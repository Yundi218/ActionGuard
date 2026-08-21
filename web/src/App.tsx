import { useEffect, useMemo, useRef, useState } from "react";

import { ConversationPane } from "./components/ConversationPane";
import { ExecutionInspector } from "./components/ExecutionInspector";
import { Sidebar } from "./components/Sidebar";
import { replacementScenario } from "./demo/replacementScenario";
import "./app.css";

export default function App() {
  const [started, setStarted] = useState(false);
  const [visibleSteps, setVisibleSteps] = useState(0);
  const progressionTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearInterval(progressionTimer.current), []);

  const scenario = useMemo(() => {
    const complete = visibleSteps >= replacementScenario.plan.length;
    return {
      ...replacementScenario,
      status: complete ? replacementScenario.status : "running" as const,
      plan: replacementScenario.plan.slice(0, visibleSteps),
      toolCalls: replacementScenario.toolCalls.slice(0, visibleSteps),
    };
  }, [visibleSteps]);

  const runDemo = () => {
    window.clearInterval(progressionTimer.current);
    setStarted(true);
    setVisibleSteps(1);
    progressionTimer.current = window.setInterval(() => {
      setVisibleSteps((current) => {
        const next = Math.min(current + 1, replacementScenario.plan.length);
        if (next === replacementScenario.plan.length) {
          window.clearInterval(progressionTimer.current);
        }
        return next;
      });
    }, 180);
  };

  const resetDemo = () => {
    window.clearInterval(progressionTimer.current);
    setVisibleSteps(0);
    setStarted(false);
  };

  return (
    <div className="app-shell">
      <Sidebar />
      <div className="workspace">
        <ConversationPane
          scenario={scenario}
          started={started}
          onRun={runDemo}
          onReset={resetDemo}
        />
        <ExecutionInspector scenario={scenario} started={started} />
      </div>
    </div>
  );
}
