"use client";

import { useEffect } from "react";

/**
 * The page's motion, in one place and without a library:
 *
 * - `[data-reveal]` elements get `is-in` the first time they scroll into view;
 *   the CSS decides what that animates.
 * - The header gets `data-scrolled` once the page leaves the top.
 * - The hero follows the pointer (depth parallax) and eases out as it scrolls
 *   away, through `--px`, `--py` and `--sp`.
 * - Cards track the pointer for their spotlight and tilt through `--mx`, `--my`,
 *   `--rx` and `--ry`.
 * - `time[data-local-time]` switches from UTC to the visitor's zone, and
 *   `[data-countdown]` clocks tick every second.
 * - On legal pages the contents link for the section being read gets
 *   `aria-current`.
 *
 * With reduced motion everything is shown at once and nothing follows the
 * pointer. Renders nothing.
 */
export function Motion() {
  useEffect(() => {
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const finePointer = window.matchMedia("(pointer: fine)").matches;
    const cleanups: Array<() => void> = [];
    // Tells the layout's fallback timer that reveals are being handled.
    document.documentElement.classList.add("motion-ready");

    // ---- reveal on scroll -------------------------------------------------
    const seen = new WeakSet<Element>();
    const revealer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.classList.add("is-in");
          revealer.unobserve(entry.target);
        }
      },
      { rootMargin: "0px 0px -8% 0px", threshold: 0.12 },
    );
    const scan = () => {
      for (const element of document.querySelectorAll("[data-reveal]")) {
        if (seen.has(element)) continue;
        seen.add(element);
        if (reduced) element.classList.add("is-in");
        else revealer.observe(element);
      }
    };
    scan();
    // Client-side navigation swaps the page without reloading this component.
    const mutations = new MutationObserver(scan);
    mutations.observe(document.body, { childList: true, subtree: true });
    cleanups.push(() => {
      revealer.disconnect();
      mutations.disconnect();
    });

    // ---- header and hero ----------------------------------------------------
    const target = { x: 0, y: 0 };
    const current = { x: 0, y: 0 };
    let frame = 0;

    const paint = () => {
      frame = 0;
      const header = document.querySelector<HTMLElement>(".site-header");
      header?.toggleAttribute("data-scrolled", window.scrollY > 12);

      const hero = document.querySelector<HTMLElement>(".hero");
      if (!hero || reduced) return;
      current.x += (target.x - current.x) * 0.08;
      current.y += (target.y - current.y) * 0.08;
      const progress = Math.min(1, Math.max(0, window.scrollY / Math.max(1, hero.offsetHeight)));
      hero.style.setProperty("--px", current.x.toFixed(4));
      hero.style.setProperty("--py", current.y.toFixed(4));
      hero.style.setProperty("--sp", progress.toFixed(4));
      if (Math.abs(target.x - current.x) > 0.001 || Math.abs(target.y - current.y) > 0.001) request();
    };
    const request = () => {
      if (!frame) frame = requestAnimationFrame(paint);
    };

    const onPointer = (event: PointerEvent) => {
      if (event.pointerType !== "mouse") return;
      target.x = (event.clientX / window.innerWidth) * 2 - 1;
      target.y = (event.clientY / window.innerHeight) * 2 - 1;
      request();
    };
    window.addEventListener("scroll", request, { passive: true });
    if (finePointer && !reduced) window.addEventListener("pointermove", onPointer, { passive: true });
    request();
    cleanups.push(() => {
      cancelAnimationFrame(frame);
      window.removeEventListener("scroll", request);
      window.removeEventListener("pointermove", onPointer);
    });

    // ---- card spotlight and tilt -------------------------------------------
    if (finePointer && !reduced) {
      const onCardMove = (event: PointerEvent) => {
        const card = (event.target as Element | null)?.closest?.<HTMLElement>(".card");
        if (!card) return;
        const box = card.getBoundingClientRect();
        const x = event.clientX - box.left;
        const y = event.clientY - box.top;
        card.style.setProperty("--mx", `${x}px`);
        card.style.setProperty("--my", `${y}px`);
        card.style.setProperty("--ry", `${((x / box.width) * 2 - 1) * 3}deg`);
        card.style.setProperty("--rx", `${((y / box.height) * 2 - 1) * -3}deg`);
      };
      const onCardLeave = (event: PointerEvent) => {
        const card = (event.target as Element | null)?.closest?.<HTMLElement>(".card");
        if (!card || card.contains(event.relatedTarget as Node | null)) return;
        card.style.setProperty("--rx", "0deg");
        card.style.setProperty("--ry", "0deg");
      };
      document.addEventListener("pointermove", onCardMove, { passive: true });
      document.addEventListener("pointerout", onCardLeave, { passive: true });
      cleanups.push(() => {
        document.removeEventListener("pointermove", onCardMove);
        document.removeEventListener("pointerout", onCardLeave);
      });
    }

    // ---- clocks: local start times and live countdowns ---------------------
    // The server renders times in UTC; the visitor reads them in their own zone.
    const localize = () => {
      for (const element of document.querySelectorAll<HTMLTimeElement>("time[data-local-time]")) {
        if (element.dataset.localized) continue;
        const at = new Date(element.dateTime);
        if (Number.isNaN(at.getTime())) continue;
        const time = at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        element.textContent =
          element.dataset.localTime === "datetime"
            ? `${at.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" })} · ${time}`
            : time;
        element.dataset.localized = "true";
      }
    };
    const tick = () => {
      localize();
      for (const clock of document.querySelectorAll<HTMLElement>("[data-countdown]")) {
        const remaining = Math.max(0, new Date(clock.dataset.countdown ?? "").getTime() - Date.now());
        const seconds = Math.floor(remaining / 1000);
        const values: Record<string, number> = {
          days: Math.floor(seconds / 86400),
          hours: Math.floor((seconds % 86400) / 3600),
          minutes: Math.floor((seconds % 3600) / 60),
          seconds: seconds % 60,
        };
        for (const unit of clock.querySelectorAll<HTMLElement>("[data-unit]")) {
          const next = String(values[unit.dataset.unit ?? ""] ?? 0).padStart(2, "0");
          if (unit.textContent !== next) unit.textContent = next;
        }
      }
    };
    tick();
    const clock = window.setInterval(tick, 1000);
    cleanups.push(() => window.clearInterval(clock));

    // ---- legal pages: highlight the section being read ---------------------
    let spy: IntersectionObserver | null = null;
    let watched: HTMLElement[] = [];
    const watchSections = () => {
      const sections = [...document.querySelectorAll<HTMLElement>("[data-legal-section]")];
      if (sections.length === watched.length && sections.every((section, index) => section === watched[index])) return;
      spy?.disconnect();
      watched = sections;
      if (sections.length === 0) return;
      const visible = new Set<string>();
      spy = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            if (entry.isIntersecting) visible.add(entry.target.id);
            else visible.delete(entry.target.id);
          }
          // The topmost section still on screen is the one being read.
          const current = sections.find((section) => visible.has(section.id))?.id;
          if (!current) return;
          for (const link of document.querySelectorAll<HTMLAnchorElement>(".legal-toc a")) {
            if (link.hash === `#${current}`) link.setAttribute("aria-current", "true");
            else link.removeAttribute("aria-current");
          }
        },
        { rootMargin: "-90px 0px -55% 0px" },
      );
      sections.forEach((section) => spy?.observe(section));
    };
    watchSections();
    const sectionChanges = new MutationObserver(watchSections);
    sectionChanges.observe(document.body, { childList: true, subtree: true });
    cleanups.push(() => {
      sectionChanges.disconnect();
      spy?.disconnect();
    });

    return () => cleanups.forEach((cleanup) => cleanup());
  }, []);

  return null;
}
