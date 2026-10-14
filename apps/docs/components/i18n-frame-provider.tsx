'use client';

import { FrameworkProvider } from "fumadocs-core/framework";
import NextImage from "next/image";
import NextLink from "next/link";
import { useParams, usePathname, useRouter } from "next/navigation";
import type { ComponentProps, ReactNode } from "react";
import { i18n } from "@/lib/i18n";

// SSG prerenders every page under its canonical `[lang]` route (`/en/agents`),
// but the URL a visitor sees for the hidden default locale has no `/en`
// (`/agents`) — the i18n middleware rewrites it. Fumadocs resolves the page
// tree path with `usePathname()`, and the page tree stores default-locale
// URLs without the prefix, so the two sides disagreed: the prerendered HTML
// found no tree entry (rendering fallbacks like "On this page" in the TOC
// popover trigger) while the client found one ("Agents"), and hydration
// failed with a React #418 text mismatch that regenerated the whole tree on
// every default-locale page load. Expose the locale-less pathname to
// fumadocs on both sides so prerender and hydration agree; non-default
// locales keep their prefix, which the tree stores as-is.
function useDocsPathname(): string {
  const pathname = usePathname() ?? "";
  const prefix = `/${i18n.defaultLanguage}`;
  if (pathname === prefix) return "/";
  if (pathname.startsWith(`${prefix}/`)) return pathname.slice(prefix.length);
  return pathname;
}

// Fumadocs' framework contract wants plain function components; adapt the
// next.js ones to it.
function DocsLink({ href, prefetch, ...rest }: ComponentProps<"a"> & { prefetch?: boolean }) {
  return <NextLink href={href ?? ""} prefetch={prefetch} {...rest} />;
}

type DocsImageProps = Omit<ComponentProps<"img">, "src"> & {
  src?: ComponentProps<typeof NextImage>["src"];
  priority?: boolean;
};

function DocsImage({ src, alt = "", priority, ...rest }: DocsImageProps) {
  // next/image requires a string src with dimensions known to the bundler;
  // fumadocs only mounts it for content images, so anything else falls back
  // to a plain <img> like fumadocs' own no-framework default.
  if (!src || typeof src !== "string") {
    // eslint-disable-next-line @next/next/no-img-element
    return <img alt={alt} {...rest} />;
  }
  // The img-attribute spread predates next/image's stricter width/height
  // value types; the runtime values fumadocs passes are the compatible subset.
  return <NextImage {...(rest as Omit<ComponentProps<typeof NextImage>, "src" | "alt" | "priority">)} src={src} alt={alt} priority={priority} />;
}

export function DocsFrameworkProvider({ children }: { children: ReactNode }) {
  return (
    <FrameworkProvider
      Link={DocsLink}
      Image={DocsImage}
      useRouter={useRouter}
      usePathname={useDocsPathname}
      useParams={useParams}
    >
      {children}
    </FrameworkProvider>
  );
}
