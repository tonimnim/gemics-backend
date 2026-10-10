/** The lit header strip that opens every inner page: title, one line of status, then its controls. */
export function PageHead({ title, meta, children }: { title: string; meta?: string | null; children?: React.ReactNode }) {
  return (
    <section className="page-hero" aria-labelledby="page-title">
      <div className="page-hero-glow" aria-hidden="true" />
      <div className="shell">
        <div className="page-hero-row">
          <h1 className="page-title" id="page-title">
            {title}
          </h1>
          {meta && <p className="page-meta">{meta}</p>}
        </div>
        {children}
      </div>
    </section>
  );
}
