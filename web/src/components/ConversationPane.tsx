import { ArrowUp, CheckCircle2, RotateCcw, ShieldCheck, Sparkles } from "lucide-react";

import type { RunScenario } from "../domain/run";

interface ConversationPaneProps {
  scenario: RunScenario;
  started: boolean;
  onRun: () => void;
  onReset: () => void;
}

export function ConversationPane({ scenario, started, onRun, onReset }: ConversationPaneProps) {
  return (
    <main className="conversation-pane">
      <header className="pane-header">
        <div>
          <div className="breadcrumb"><span>Agent runs</span><span>/</span><strong>{started ? scenario.id : "New run"}</strong></div>
          <h1>{started ? scenario.title : "New policy-constrained run"}</h1>
        </div>
        {started ? (
          <button className="icon-button" type="button" onClick={onReset} title="Reset demo" aria-label="Reset demo">
            <RotateCcw size={17} />
          </button>
        ) : null}
      </header>

      <section className={`conversation-body${started ? " active-run" : ""}`}>
        {!started ? (
          <div className="empty-run">
            <span className="empty-run-icon"><ShieldCheck size={27} /></span>
            <h2>No run selected</h2>
            <p>Damaged headphones replacement · CN retail after-sales</p>
            <button className="primary-button" type="button" onClick={onRun}>
              <Sparkles size={16} />
              Run demo
            </button>
          </div>
        ) : (
          <div className="message-stack">
            <article className="message user-message">
              <div className="message-avatar user-avatar">CY</div>
              <div>
                <div className="message-meta"><strong>You</strong><span>10:42:16</span></div>
                <p>{scenario.userMessage}</p>
              </div>
            </article>

            <article className="message agent-message">
              <div className="message-avatar agent-avatar"><ShieldCheck size={16} /></div>
              <div className="agent-response">
                <div className="message-meta"><strong>ActionGuard</strong><span>10:42:17</span></div>
                <p>I found order <strong>AG-1042</strong> and verified that the delivered item is covered by the damaged-goods policy.</p>
                <div className="decision-strip">
                  <CheckCircle2 size={16} />
                  <span>Plan verified against identity, scopes, policy, and current inventory.</span>
                </div>
                <p>{scenario.status === "succeeded" ? scenario.finalSummary : "Executing the verified plan against the commerce simulator..."}</p>
                <div className="citation-row">
                  <span>2 policy citations</span>
                  <span>6 tool calls</span>
                  <span>0 unsafe actions</span>
                </div>
              </div>
            </article>
          </div>
        )}
      </section>

      <footer className="composer-wrap">
        <div className="composer">
          <textarea
            aria-label="Message"
            defaultValue=""
            placeholder="Describe an after-sales request"
            rows={2}
            disabled={started}
          />
          <button className="send-button" type="button" onClick={onRun} disabled={started} title="Run message" aria-label="Run message">
            <ArrowUp size={18} />
          </button>
        </div>
        <span className="composer-note">Fixture data · no external commerce system</span>
      </footer>
    </main>
  );
}
