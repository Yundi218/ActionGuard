import {
  BookOpenText,
  Braces,
  ChartNoAxesCombined,
  PlaySquare,
  ShieldCheck,
} from "lucide-react";

const navigation = [
  { label: "Agent runs", icon: PlaySquare, active: true },
  { label: "Benchmarks", icon: ChartNoAxesCombined },
  { label: "Policies", icon: BookOpenText },
  { label: "Tools", icon: Braces },
];

export function Sidebar() {
  return (
    <aside className="sidebar">
      <div className="brand-lockup">
        <span className="brand-mark"><ShieldCheck size={19} strokeWidth={2.2} /></span>
        <span className="brand-name">ActionGuard</span>
      </div>

      <nav aria-label="Primary navigation" className="primary-nav">
        <span className="nav-label">Workspace</span>
        {navigation.map(({ label, icon: Icon, active }) => (
          <button
            className={`nav-item${active ? " active" : ""}`}
            key={label}
            type="button"
            aria-current={active ? "page" : undefined}
            title={label}
          >
            <Icon size={17} />
            <span>{label}</span>
          </button>
        ))}
      </nav>

      <div className="sidebar-footer">
        <div className="environment-state">
          <span className="environment-dot" />
          <div>
            <strong>Demo environment</strong>
            <span>Deterministic provider</span>
          </div>
        </div>
      </div>
    </aside>
  );
}
