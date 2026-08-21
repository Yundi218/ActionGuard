import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import App from "./App";

describe("ActionGuard workbench", () => {
  it("reveals the policy-constrained replacement trace", async () => {
    const user = userEvent.setup();
    render(<App />);

    expect(screen.getByText("No run selected")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("");
    await user.click(screen.getByRole("button", { name: /run demo/i }));

    expect(screen.getByText("running")).toBeInTheDocument();
    expect(within(screen.getByRole("tabpanel")).getByText("get_order")).toBeInTheDocument();
    expect(await within(screen.getByRole("tabpanel")).findByText("create_replacement", {}, { timeout: 3000 })).toBeInTheDocument();
    expect(await screen.findByText("succeeded", {}, { timeout: 3000 })).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: /policy evidence/i }));
    expect(screen.getByText("damaged_goods:v3:4.2:180-412")).toBeInTheDocument();
  });

  it("keeps shipment notes in an untrusted container", async () => {
    const user = userEvent.setup();
    render(<App />);

    await user.click(screen.getByRole("button", { name: /run demo/i }));
    await user.click(screen.getByRole("tab", { name: /tool trace/i }));

    expect(await screen.findByLabelText("Untrusted tool text", {}, { timeout: 3000 })).toHaveTextContent(
      /ignore previous rules/i,
    );
  });

  it("supports arrow-key navigation between inspector tabs", async () => {
    const user = userEvent.setup();
    render(<App />);

    const planTab = screen.getByRole("tab", { name: "Plan" });
    planTab.focus();
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "Policy evidence" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "Policy evidence" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    for (const tab of screen.getAllByRole("tab")) {
      expect(document.getElementById(tab.getAttribute("aria-controls")!)).toBeInTheDocument();
    }
  });

  it("does not expose final business state before execution succeeds", async () => {
    const user = userEvent.setup();
    render(<App />);

    await user.click(screen.getByRole("button", { name: /run demo/i }));
    await user.click(screen.getByRole("tab", { name: "Final state" }));

    expect(screen.getByText("Execution in progress")).toBeInTheDocument();
    expect(screen.queryByText("RPL-8041 · created")).not.toBeInTheDocument();
    expect(await screen.findByText("RPL-8041 · created", {}, { timeout: 3000 })).toBeInTheDocument();
  });
});
