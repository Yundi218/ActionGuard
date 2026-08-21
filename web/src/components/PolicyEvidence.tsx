import { ExternalLink, Filter, Quote } from "lucide-react";

import type { PolicyEvidenceItem } from "../domain/run";

export function PolicyEvidence({ evidence }: { evidence: PolicyEvidenceItem[] }) {
  return (
    <div className="evidence-view">
      <div className="filter-row"><Filter size={14} /><span>CN</span><span>electronics</span><span>effective 2026-08-21</span></div>
      {evidence.map((item, index) => (
        <article className="evidence-item" key={item.citationId}>
          <div className="evidence-heading">
            <span className="evidence-index">0{index + 1}</span>
            <div><strong>{item.policyId}:{item.version}</strong><span>{item.section}</span></div>
            <span className="link-icon" title="Stable source citation" aria-hidden="true"><ExternalLink size={14} /></span>
          </div>
          <blockquote><Quote size={14} />{item.excerpt}</blockquote>
          <div className="evidence-footer"><code>{item.citationId}</code><span>offset {item.offsets[0]}–{item.offsets[1]}</span></div>
        </article>
      ))}
    </div>
  );
}
