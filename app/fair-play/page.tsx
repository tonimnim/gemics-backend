import type { Metadata } from "next";
import { LegalPage } from "../legal-page";
import { fairPlay } from "../lib/legal-docs";

export const metadata: Metadata = {
  title: `${fairPlay.title} — Ozone`,
  description: fairPlay.description,
};

export default function Page() {
  return <LegalPage doc={fairPlay} path="/fair-play" />;
}
