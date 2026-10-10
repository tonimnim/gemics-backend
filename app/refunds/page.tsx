import type { Metadata } from "next";
import { LegalPage } from "../legal-page";
import { refunds } from "../lib/legal-docs";

export const metadata: Metadata = {
  title: `${refunds.title} — Tonits`,
  description: refunds.description,
};

export default function Page() {
  return <LegalPage doc={refunds} path="/refunds" />;
}
