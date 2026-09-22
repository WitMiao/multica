package lark

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// typingEmoji is the Lark emoji_type used for the "processing" indicator.
// It renders as a small typing-animation badge on the message.
const typingEmoji = "Typing"

// typingIndicatorMaxAge is how old a message can be before we skip the
// typing indicator. This prevents stale reactions when a WebSocket
// reconnect replays old events. Aligned with OpenClaw's 2-minute bound.
const typingIndicatorMaxAge = 2 * time.Minute

// typingCleanupTimeout bounds best-effort cleanup independently of reply delivery.
const typingCleanupTimeout = 2 * time.Second

// TypingIndicatorState holds the identifiers needed to remove a reaction, plus
// the installation whose app credentials added it. The installation id is
// recorded at add time because that is the last moment it is certainly
// resolvable: it is reachable from the session's channel_chat_session_binding
// row, and a session delete drops that row while the cancel it triggers is
// still on its way to the Patcher.
type TypingIndicatorState struct {
	MessageID      string
	ReactionID     string
	InstallationID pgtype.UUID

	// ended is set under mu when Clear takes this add, even before HTTP returns.
	ended bool

	// installSnapshot is the installation row as it stood when the reaction was
	// added, kept for the one case where the id is no longer enough: a runtime
	// teardown deletes the installation inside the same transaction that
	// cancels the tasks (handler/runtime.go,
	// DeleteChannelInstallationsBySystemRuntimeAgents), so by the time the
	// cancel reaches Clear there is no row to resolve.
	//
	// It is a FALLBACK, never the primary. A live lookup picks up a credential
	// rotation between add and clear; a snapshot cannot, so it is consulted
	// only when the row is genuinely gone.
	//
	// It does not weaken "no decrypted secret lives in the state map": what is
	// held here is the same encrypted blob the database holds, and
	// DecryptAppSecret still runs at clear time.
	installSnapshot Installation
}

// TypingIndicatorQueries is the narrow DB surface the manager needs.
type TypingIndicatorQueries interface {
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
}

// TypingIndicatorManager owns the "processing" reaction lifecycle for
// inbound Lark messages. When a message is successfully ingested it adds
// a Typing reaction; when the run ends — with a reply, a failure or a
// cancellation — it clears the reaction(s) for that chat session.
//
// The manager is safe for concurrent use. It tolerates missing or
// stale state gracefully: adding a reaction to a message that already
// has one simply appends another state entry; clearing a session with
// no tracked state is a no-op.
type TypingIndicatorManager struct {
	client      APIClient
	credentials CredentialsResolver
	queries     TypingIndicatorQueries
	log         *slog.Logger

	mu     sync.RWMutex
	states map[string][]*TypingIndicatorState // key = chat_session_id string
}

// NewTypingIndicatorManager constructs a manager. All dependencies must
// be non-nil; the manager panics on nil client / credentials / queries.
func NewTypingIndicatorManager(client APIClient, credentials CredentialsResolver, queries TypingIndicatorQueries, log *slog.Logger) *TypingIndicatorManager {
	if log == nil {
		log = slog.Default()
	}
	return &TypingIndicatorManager{
		client:      client,
		credentials: credentials,
		queries:     queries,
		log:         log,
		states:      make(map[string][]*TypingIndicatorState),
	}
}

// Add sends a Typing reaction to the given message and records the state
// under the chat session. It is synchronous — the caller decides whether
// to run it in a detached goroutine. Errors are logged and swallowed.
//
// createTime is Lark's epoch-millisecond string (InboundMessage.CreateTime).
// Messages older than typingIndicatorMaxAge are silently skipped so that
// WebSocket replays and stale reconnects do not surface misleading "processing"
// badges on long-finished conversations.
func (m *TypingIndicatorManager) Add(ctx context.Context, inst Installation, chatSessionID pgtype.UUID, messageID string, createTime string) {
	if messageID == "" {
		return
	}
	if isMessageTooOld(createTime) {
		m.log.Debug("lark typing indicator: message too old, skipping",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"create_time", createTime,
		)
		return
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	key := uuidString(chatSessionID)
	state := &TypingIndicatorState{MessageID: messageID, InstallationID: inst.ID, installSnapshot: inst}
	m.mu.Lock()
	m.states[key] = append(m.states[key], state)
	m.mu.Unlock()

	reactionID, err := m.client.AddMessageReaction(ctx, AddReactionParams{
		InstallationID: creds,
		MessageID:      messageID,
		EmojiType:      typingEmoji,
	})
	if err != nil {
		m.mu.Lock()
		for i, pending := range m.states[key] {
			if pending == state {
				m.states[key] = append(m.states[key][:i], m.states[key][i+1:]...)
				if len(m.states[key]) == 0 {
					delete(m.states, key)
				}
				break
			}
		}
		m.mu.Unlock()
		m.log.Warn("lark typing indicator: add reaction failed",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	m.mu.Lock()
	ended := state.ended
	if !ended {
		state.ReactionID = reactionID
	}
	m.mu.Unlock()
	if ended {
		// Clear owns this add's generation. Do not attach its late result to a
		// newer turn, and do not reuse an add context that may have expired.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), typingCleanupTimeout)
		defer cancel()
		if err := m.client.DeleteMessageReaction(cleanupCtx, DeleteReactionParams{
			InstallationID: creds, MessageID: messageID, ReactionID: reactionID,
		}); err != nil {
			m.log.Warn("lark typing indicator: late add cleanup failed", "message_id", messageID, "err", err)
		}
		return
	}

	m.log.Debug("lark typing indicator: reaction added",
		"chat_session_id", key,
		"message_id", messageID,
		"reaction_id", reactionID,
	)
}

// Clear removes every tracked Typing reaction for the chat session and
// drops the state entry. Pending adds are marked ended so their late results
// remove themselves. Calls are synchronous and bounded by the caller's context.
// Individual delete failures are logged but do not abort the loop.
//
// Credentials come from the installation each state recorded, not from the
// session's binding, because a clear can outlive that binding: deleting a chat
// session drops the binding row inside the same transaction that cancels the
// session's tasks, and the task:cancelled events that reach the Patcher are
// broadcast after that transaction commits. A binding lookup would miss, and
// since the state has already been taken here, there would be nothing left to
// clear from. Installation rows survive a session delete.
//
// They do NOT survive a runtime teardown, which deletes them in the same
// transaction — so each state also carries the installation as it stood at add
// time, consulted only when the row is gone. See TypingIndicatorState.
func (m *TypingIndicatorManager) Clear(ctx context.Context, chatSessionID pgtype.UUID) {
	key := uuidString(chatSessionID)
	m.mu.Lock()
	states := m.states[key]
	for _, state := range states {
		state.ended = true
	}
	delete(m.states, key)
	m.mu.Unlock()

	if len(states) == 0 {
		return
	}

	// One session's reactions normally share an installation, so the resolved
	// credentials are memoised; a session rebound to another installation
	// mid-run still clears every reaction through the app that added it. A nil
	// entry records an installation that failed to resolve, so it is not
	// retried once per reaction.
	resolved := make(map[string]*InstallationCredentials, 1)
	for _, s := range states {
		if s.ReactionID == "" {
			continue
		}
		instKey := uuidString(s.InstallationID)
		creds, seen := resolved[instKey]
		if !seen {
			creds = m.credentialsForInstallation(ctx, key, s.InstallationID, s.installSnapshot)
			resolved[instKey] = creds
		}
		if creds == nil {
			continue
		}
		if err := m.client.DeleteMessageReaction(ctx, DeleteReactionParams{
			InstallationID: *creds,
			MessageID:      s.MessageID,
			ReactionID:     s.ReactionID,
		}); err != nil {
			m.log.Warn("lark typing indicator: delete reaction failed",
				"chat_session_id", key,
				"message_id", s.MessageID,
				"reaction_id", s.ReactionID,
				"err", err,
			)
			continue
		}
		m.log.Debug("lark typing indicator: reaction removed",
			"chat_session_id", key,
			"message_id", s.MessageID,
			"reaction_id", s.ReactionID,
		)
	}
}

// SweepMessage removes every Typing reaction the bot itself put on a message,
// found by listing the message's reactions from Lark rather than by consulting
// the in-memory state. This is the authoritative half of the lifecycle: the
// state map only exists in the process that ran Add, so a restart between add
// and clear or a second replica handling completion leaves the map empty
// while the badge is still on screen. In-flight adds are handled separately
// by the ended marker in Add/Clear. The sweep also retries reactions whose
// exact deletion failed in Clear.
//
// It deletes only reactions whose operator_type is "app". Lark lets a bot
// delete solely what it added, so a human's own Typing reaction is untouchable
// anyway; the filter keeps the sweep from burning delete calls (and warning
// logs) on reactions it could never remove.
//
// credentials came from the caller, which resolves them from the same
// installation that added the reaction; errors are logged and swallowed — the
// indicator is best-effort and must never fail a reply. A client that does not
// implement ReactionLister (the stub, minimal fakes) skips the sweep.
func (m *TypingIndicatorManager) SweepMessage(ctx context.Context, creds InstallationCredentials, messageID string) {
	if messageID == "" {
		return
	}
	lister, ok := m.client.(ReactionLister)
	if !ok {
		m.log.Debug("lark typing indicator: client cannot list reactions, skipping sweep",
			"message_id", messageID,
		)
		return
	}
	reactions, err := lister.ListMessageReactions(ctx, ListMessageReactionsParams{
		InstallationID: creds,
		MessageID:      messageID,
		EmojiType:      typingEmoji,
	})
	if err != nil {
		m.log.Warn("lark typing indicator: sweep list reactions failed",
			"message_id", messageID,
			"err", err,
		)
		return
	}
	for _, r := range reactions {
		if !strings.EqualFold(r.EmojiType, typingEmoji) || r.OperatorType != "app" {
			continue
		}
		if err := m.client.DeleteMessageReaction(ctx, DeleteReactionParams{
			InstallationID: creds,
			MessageID:      messageID,
			ReactionID:     r.ReactionID,
		}); err != nil {
			m.log.Warn("lark typing indicator: sweep delete reaction failed",
				"message_id", messageID,
				"reaction_id", r.ReactionID,
				"err", err,
			)
			continue
		}
		m.log.Debug("lark typing indicator: sweep removed reaction",
			"message_id", messageID,
			"reaction_id", r.ReactionID,
		)
	}
}

// credentialsForInstallation loads an installation row and decrypts its app
// secret. Nil means the clear cannot proceed for that installation; the reason
// is already logged. The decrypted secret exists only from here on, never in the
// state map.
func (m *TypingIndicatorManager) credentialsForInstallation(ctx context.Context, sessionKey string, id pgtype.UUID, snapshot Installation) *InstallationCredentials {
	inst, err := m.queries.GetLarkInstallation(ctx, id)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows) && snapshot.ID.Valid:
		// The row is gone, which on this path means the runtime teardown
		// deleted it in the transaction that cancelled these tasks. The
		// reaction it added is still on the message and this is the only thing
		// left that can take it off.
		m.log.Debug("lark typing indicator: installation gone, clearing from the snapshot taken at add time",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
		)
		inst = snapshot
	default:
		m.log.Warn("lark typing indicator: failed to lookup installation for clear",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
			"err", err,
		)
		return nil
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials for clear",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
			"err", err,
		)
		return nil
	}
	return &creds
}

func isMessageTooOld(createTime string) bool {
	if createTime == "" {
		return false
	}
	ms, err := strconv.ParseInt(createTime, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.UnixMilli(ms)) > typingIndicatorMaxAge
}

func (m *TypingIndicatorManager) resolveCredentials(inst Installation) (InstallationCredentials, error) {
	secret, err := m.credentials.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, err
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}
