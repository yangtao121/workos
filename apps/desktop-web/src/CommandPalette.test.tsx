// @vitest-environment jsdom

// Command Palette unit behavior (W6): bounded results, keyboard navigation,
// and the fixed stale verdict for revalidated-dead targets — never a
// fallback navigation.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach } from "vitest";
import { describe, expect, it, vi } from "vitest";

import { CommandPalette, type PaletteAction } from "./CommandPalette.js";

function action(id: string, label: string, run: PaletteAction["run"]): PaletteAction {
  return { id, label, hint: "window", run };
}

afterEach(cleanup);

describe("CommandPalette", () => {
  it("filters to a bounded result set and navigates with the keyboard", async () => {
    const runs = [
      vi.fn(() => Promise.resolve("ok" as const)),
      vi.fn(() => Promise.resolve("ok" as const)),
    ];
    const actions: PaletteAction[] = [
      action("a", "Open Alpha", runs[0] as unknown as PaletteAction["run"]),
      action("b", "Open Beta", runs[1] as unknown as PaletteAction["run"]),
      {
        id: "stale",
        label: "Switch to project: vanished",
        hint: "project",
        run: () => Promise.resolve("stale" as const),
      },
    ];
    const onClose = vi.fn();
    render(<CommandPalette actions={actions} onClose={onClose} />);

    const input = screen.getByLabelText("Search commands");
    fireEvent.change(input, { target: { value: "open" } });
    const options = await screen.findAllByRole("option");
    expect(options.length).toBe(2);

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "ArrowDown" });
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" });
    await waitFor(() => {
      expect(runs[1]).toHaveBeenCalled();
    });
    expect(onClose).toHaveBeenCalled();
  });

  it("shows the fixed stale verdict and never closes on a dead target", async () => {
    const actions: PaletteAction[] = [
      {
        id: "switch",
        label: "Switch to project: vanished",
        hint: "project",
        run: () => Promise.resolve("stale" as const),
      },
    ];
    const onClose = vi.fn();
    render(<CommandPalette actions={actions} onClose={onClose} />);
    fireEvent.click(screen.getByText("Switch to project: vanished"));
    const status = await screen.findByRole("status");
    expect(status.textContent).toBe("This action is no longer available.");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    render(<CommandPalette actions={[]} onClose={onClose} />);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("keeps rejected actions retryable and prevents duplicate execution", async () => {
    let reject!: (reason: Error) => void;
    const run = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise((_resolve, fail) => {
            reject = fail;
          }),
      )
      .mockResolvedValue("ok");
    const onClose = vi.fn();
    render(<CommandPalette actions={[action("open", "Open Files", run)]} onClose={onClose} />);
    const dialog = screen.getByRole("dialog");
    fireEvent.keyDown(dialog, { key: "Enter" });
    fireEvent.keyDown(dialog, { key: "Enter" });
    expect(run).toHaveBeenCalledTimes(1);
    reject(new Error("private server details"));
    expect((await screen.findByRole("status")).textContent).toBe(
      "Could not complete this action. Try again.",
    );
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.keyDown(dialog, { key: "Enter" });
    await waitFor(() => {
      expect(onClose).toHaveBeenCalledOnce();
    });
    expect(run).toHaveBeenCalledTimes(2);
  });

  it("traps keyboard focus and returns it to the launcher", () => {
    const launcher = document.createElement("button");
    document.body.append(launcher);
    launcher.focus();
    const view = render(
      <CommandPalette
        actions={[action("open", "Open Files", () => Promise.resolve("ok"))]}
        onClose={() => undefined}
      />,
    );
    const input = screen.getByLabelText("Search commands");
    expect(document.activeElement).toBe(input);
    fireEvent.keyDown(input, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open Files window" }));
    fireEvent.keyDown(screen.getByRole("button", { name: "Open Files window" }), { key: "Tab" });
    expect(document.activeElement).toBe(input);
    view.unmount();
    expect(document.activeElement).toBe(launcher);
    launcher.remove();
  });
});

it("keeps Enter usable when refreshed actions shrink below the selection", async () => {
  const run = vi.fn(() => Promise.resolve("ok" as const));
  const first = action("a", "Open Alpha", run);
  const view = render(
    <CommandPalette actions={[first, action("b", "Open Beta", run)]} onClose={vi.fn()} />,
  );
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "ArrowDown" });
  view.rerender(<CommandPalette actions={[first]} onClose={vi.fn()} />);
  expect(screen.getByRole("option").getAttribute("aria-selected")).toBe("true");
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" });
  await waitFor(() => {
    expect(run).toHaveBeenCalledOnce();
  });
});
