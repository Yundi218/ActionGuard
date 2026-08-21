export type RunStatus =
  | "ready"
  | "running"
  | "succeeded"
  | "waiting_approval"
  | "failed";

export type StepRisk = "read" | "write" | "high_risk_write";

export interface PlanStep {
  id: string;
  tool: string;
  risk: StepRisk;
  dependsOn: string[];
  status: "completed" | "waiting" | "blocked";
  durationMs?: number;
}

export interface PolicyEvidenceItem {
  citationId: string;
  policyId: string;
  version: string;
  section: string;
  excerpt: string;
  region: string;
  productCategory: string;
  effectiveAt: string;
  offsets: [number, number];
}

export interface ToolCall {
  id: string;
  tool: string;
  status: "completed" | "replayed" | "blocked";
  durationMs: number;
  trustedFields: Record<string, string>;
  untrustedText?: string;
}

export interface RunScenario {
  id: string;
  title: string;
  userMessage: string;
  status: RunStatus;
  evidence: PolicyEvidenceItem[];
  plan: PlanStep[];
  verification: {
    valid: boolean;
    checks: string[];
  };
  toolCalls: ToolCall[];
  finalSummary: string;
  finalState: Array<{ label: string; value: string; tone?: "positive" | "neutral" }>;
}
