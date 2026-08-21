import { Check, LockKeyhole, ShieldAlert } from "lucide-react";

import type { RunScenario } from "../domain/run";

export function PlanGraph({ scenario }: { scenario: RunScenario }) {
  return (
    <div className="plan-view">
      <div className="verification-banner">
        <span className="verification-icon"><Check size={15} /></span>
        <div><strong>Verified plan</strong><span>{scenario.verification.checks.length} deterministic checks passed</span></div>
      </div>
      <ol className="plan-list">
        {scenario.plan.map((step, index) => (
          <li className="plan-step" key={step.id}>
            <div className="step-rail">
              <span className="step-number"><Check size={13} /></span>
              {index < scenario.plan.length - 1 ? <span className="rail-line" /> : null}
            </div>
            <div className="step-main">
              <div className="step-title-row">
                <code>{step.tool}</code>
                <span className={`risk-badge ${step.risk}`}>
                  {step.risk === "high_risk_write" ? <ShieldAlert size={12} /> : step.risk === "write" ? <LockKeyhole size={12} /> : null}
                  {step.risk.replaceAll("_", " ")}
                </span>
              </div>
              <div className="step-detail">
                <span>{step.id}</span>
                <span>{step.dependsOn.length ? `after ${step.dependsOn.join(", ")}` : "entry"}</span>
                <span>{step.durationMs} ms</span>
              </div>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}
