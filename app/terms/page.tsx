import type { Metadata } from "next";
import { LegalPage } from "../legal-page";
import { terms } from "../lib/legal-docs";

export const metadata: Metadata = {
  title: `${terms.title} — Tonits`,
  description: terms.description,
};

export default function Page() {
  return <LegalPage doc={terms} path="/terms" />;
}
