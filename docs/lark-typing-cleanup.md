# Lark Typing reaction cleanup

Before sending an Add request, the server records each input in
`channel_typing_reaction`, including the source message, session, workspace,
installation and encrypted credential snapshot. A failed registration prevents
the Add. Cleanup records survive deletion of the original session or installation.

Terminal processing enumerates every input in the shared ledger, including
coalesced inputs that are not the reply delivery's trigger. Cleanup ownership is
monotonic: an Add returning after another replica observes termination cannot
restore an active state. Failed flushes persist an input cutoff and context
revision before detached ingestion, so taskless settlement also works when it
precedes Add registration. Later inputs and rolled-back transactions do not
authorize cleanup of the wrong turn.

Known reaction IDs are deleted precisely. Unknown Add outcomes are reconciled by
listing app-owned Typing reactions on the recorded message; human reactions are
preserved. A completed empty sweep does not permanently acknowledge an unfinished
Add. Unregistered reactions from older server versions retain the delivery-anchor
fallback, but that fallback cannot reconstruct every historical input.

## Retry and retention limits

- The worker runs every 30 seconds, claims at most 100 rows using `SKIP LOCKED`,
  and applies a 30-second to one-hour backoff. Its pass has a ten-second budget;
  each remote operation has a two-second budget. It does not hold database locks
  across remote calls.
- Claims advance retry counters before remote execution. A claimed row that does
  not fit the pass budget is deferred too; the counter measures claims, not HTTP
  attempts. Backlog latency and throughput are not guaranteed.
- Confirmed cleanup records become eligible for deletion after seven days.
  Pruning shares the retry pass deadline and runs approximately hourly. Sustained
  failures can starve pruning; deletion is not batched. Seven days is not a hard
  retention limit.
- Unconfirmed Adds and permanent failures have no automatic expiry, dead-letter
  policy or per-workspace quota. Their credential snapshots may remain indefinitely.
  Current installation credentials are preferred; deleted installations rely on
  the encrypted snapshot. Preserve keys needed to decrypt those snapshots.
- Revoked credentials, lost encryption keys or permanently unavailable remote APIs
  prevent guaranteed eventual cleanup. A failed taskless settlement write followed
  by a successful Add also lacks a committed terminal marker. These limitations
  require further operational design; low-volume testing does not establish safe
  sustained operation.

## Migrations and rollback

Migration 536 adds the ledger and taskless settlement flag. Migrations 537–539
build the three indexes concurrently and register invalid-index cleanup hooks.
Interrupted builds are dropped and rebuilt before being marked applied; valid
indexes are preserved on retry. There are no new foreign keys or cascading deletes.

Validate upgrades using the complete historical schema, retained input rows,
repeated `up`, and interrupted-index recovery. Four migrations against a minimal
table fixture alone do not establish full upgrade compatibility.

Application rollback preserves the schema, ledger and encryption keys. An older
server will not run this compensation worker. Bare `migrate down` rolls back all
applied migrations in the directory, not just these four, and must not be used as
an application rollback shortcut. Review pending cleanup records before any
separately planned schema removal.
