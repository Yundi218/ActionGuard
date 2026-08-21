import { AlertTriangle, Check, Clock3 } from "lucide-react";

import type { ToolCall } from "../domain/run";

export function ToolTrace({ toolCalls }: { toolCalls: ToolCall[] }) {
  return (
    <div className="trace-view">
      {toolCalls.map((call) => (
        <details className="trace-call" key={call.id} open={Boolean(call.untrustedText)}>
          <summary>
            <span className="trace-status"><Check size={12} /></span>
            <code>{call.tool}</code>
            <span className="trace-duration"><Clock3 size={12} />{call.durationMs} ms</span>
          </summary>
          <div className="trace-payload">
            <span className="payload-label">Trusted fields</span>
            <dl>
              {Object.entries(call.trustedFields).map(([key, value]) => (
                <div key={key}><dt>{key}</dt><dd>{value}</dd></div>
              ))}
            </dl>
            {call.untrustedText ? (
              <div className="untrusted-block" aria-label="Untrusted tool text">
                <div><AlertTriangle size={14} /><strong>Untrusted tool text</strong><span>Excluded from authority context</span></div>
                <p>{call.untrustedText}</p>
              </div>
            ) : null}
          </div>
        </details>
      ))}
    </div>
  );
}
