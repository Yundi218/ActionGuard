import { CheckCircle2, CircleGauge, FileCheck2, ListTree, LoaderCircle, ScrollText, Wrench } from "lucide-react";
import { useRef, useState } from "react";

import type { RunScenario } from "../domain/run";
import { PlanGraph } from "./PlanGraph";
import { PolicyEvidence } from "./PolicyEvidence";
import { ToolTrace } from "./ToolTrace";

type InspectorTab = "plan" | "evidence" | "trace" | "state";

const tabs: Array<{ id: InspectorTab; label: string; icon: typeof ListTree }> = [
  { id: "plan", label: "Plan", icon: ListTree },
  { id: "evidence", label: "Policy evidence", icon: ScrollText },
  { id: "trace", label: "Tool trace", icon: Wrench },
  { id: "state", label: "Final state", icon: FileCheck2 },
];

export function ExecutionInspector({ scenario, started }: { scenario: RunScenario; started: boolean }) {
  const [activeTab, setActiveTab] = useState<InspectorTab>("plan");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const moveTabFocus = (currentIndex: number, direction: 1 | -1) => {
    const nextIndex = (currentIndex + direction + tabs.length) % tabs.length;
    const nextTab = tabs[nextIndex];
    setActiveTab(nextTab.id);
    tabRefs.current[nextIndex]?.focus();
  };

  const panelContent = (tab: InspectorTab) => {
    if (!started) {
      return <div className="inspector-empty"><ListTree size={22} /><span>Run details will appear here</span></div>;
    }
    if (tab === "plan") {
      return <PlanGraph scenario={scenario} />;
    }
    if (tab === "evidence") {
      return <PolicyEvidence evidence={scenario.evidence} />;
    }
    if (tab === "trace") {
      return <ToolTrace toolCalls={scenario.toolCalls} />;
    }
    if (scenario.status !== "succeeded") {
      return (
        <div className="state-pending">
          <LoaderCircle size={20} />
          <div><strong>Execution in progress</strong><span>Final business state is hidden until post-condition checks pass.</span></div>
        </div>
      );
    }
    return (
      <div className="final-state-view">
        <div className="state-summary"><CheckCircle2 size={20} /><div><strong>Business state verified</strong><span>Post-condition reads match the expected snapshot.</span></div></div>
        <dl>{scenario.finalState.map((item) => <div key={item.label}><dt>{item.label}</dt><dd className={item.tone}>{item.value}</dd></div>)}</dl>
      </div>
    );
  };

  return (
    <aside className="execution-inspector">
      <header className="inspector-header">
        <div><span className="eyebrow">Execution inspector</span><h2>{started ? scenario.id : "No active run"}</h2></div>
        <span className={`run-status ${started ? scenario.status : "ready"}`}><span />{started ? scenario.status : "idle"}</span>
      </header>

      <div className="run-metrics">
        <div><CircleGauge size={15} /><span>Risk</span><strong>{started ? "guarded" : "—"}</strong></div>
        <div><CheckCircle2 size={15} /><span>Verifier</span><strong>{started ? "passed" : "—"}</strong></div>
        <div><Wrench size={15} /><span>Calls</span><strong>{started ? scenario.toolCalls.length : "—"}</strong></div>
      </div>

      <div className="inspector-tabs" role="tablist" aria-label="Run inspection views">
        {tabs.map(({ id, label, icon: Icon }, index) => (
          <button
            type="button"
            role="tab"
            id={`inspector-tab-${id}`}
            aria-controls={`inspector-panel-${id}`}
            aria-selected={activeTab === id}
            tabIndex={activeTab === id ? 0 : -1}
            className={activeTab === id ? "active" : ""}
            key={id}
            onClick={() => setActiveTab(id)}
            onKeyDown={(event) => {
              if (event.key === "ArrowRight") {
                event.preventDefault();
                moveTabFocus(index, 1);
              } else if (event.key === "ArrowLeft") {
                event.preventDefault();
                moveTabFocus(index, -1);
              }
            }}
            ref={(element) => { tabRefs.current[index] = element; }}
          >
            <Icon size={14} />
            <span>{label}</span>
          </button>
        ))}
      </div>

      <div className={`inspector-content${started ? "" : " empty"}`}>
        {tabs.map(({ id }) => (
          <div
            className="inspector-panel"
            role="tabpanel"
            id={`inspector-panel-${id}`}
            aria-labelledby={`inspector-tab-${id}`}
            hidden={activeTab !== id}
            key={id}
          >
            {panelContent(id)}
          </div>
        ))}
      </div>
    </aside>
  );
}
