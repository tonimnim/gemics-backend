import type { Metadata } from "next";
import { LegalPage } from "../legal-page";
import { privacy } from "../lib/legal-docs";

export const metadata: Metadata = {
  title: `${privacy.title} — Tonits`,
  description: privacy.description,
};

export default function Page() {
  return <LegalPage doc={privacy} path="/privacy" />;
}
