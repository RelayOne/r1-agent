// SPDX-License-Identifier: MIT
import { describe, it, expect, vi } from "vitest";
import {
  render,
  screen,
  waitFor,
  fireEvent,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NewSessionDialog } from "@/components/session/NewSessionDialog";

const MODELS = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-haiku-4-5", label: "Haiku 4.5" },
];

describe("<NewSessionDialog>", () => {
  it("renders nothing when closed", () => {
    render(
      <NewSessionDialog
        open={false}
        onOpenChange={() => {}}
        onCreate={() => {}}
        models={MODELS}
      />,
    );
    expect(screen.queryByTestId("new-session-dialog")).toBeNull();
  });

  it("renders the form fields when open", () => {
    render(
      <NewSessionDialog
        open
        onOpenChange={() => {}}
        onCreate={() => {}}
        models={MODELS}
        defaultWorkdir="/repo"
      />,
    );
    expect(screen.getByTestId("new-session-form")).toBeTruthy();
    expect(screen.getByTestId("new-session-model")).toBeTruthy();
    expect(screen.getByTestId("new-session-workdir")).toBeTruthy();
    expect(screen.getByTestId("new-session-cancel")).toBeTruthy();
    expect(screen.getByTestId("new-session-submit")).toBeTruthy();
    // Preset is hidden when no presets are passed.
    expect(screen.queryByTestId("new-session-preset")).toBeNull();
  });

  it("blocks submit and surfaces error when workdir is empty", async () => {
    const onCreate = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <NewSessionDialog
        open
        onOpenChange={onOpenChange}
        onCreate={onCreate}
        models={MODELS}
        defaultWorkdir=""
      />,
    );
    fireEvent.click(screen.getByTestId("new-session-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("new-session-workdir-error")).toBeTruthy();
    });
    expect(onCreate).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it("submits a valid CreateSessionRequest payload and closes the dialog", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();
    render(
      <NewSessionDialog
        open
        onOpenChange={onOpenChange}
        onCreate={onCreate}
        models={MODELS}
        defaultWorkdir="/repo/web"
      />,
    );
    await user.click(screen.getByTestId("new-session-submit"));
    await waitFor(() => {
      expect(onCreate).toHaveBeenCalledTimes(1);
    });
    expect(onCreate).toHaveBeenCalledWith({
      model: "claude-opus-4-7",
      workdir: "/repo/web",
    });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("trims surrounding whitespace from the workdir input", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue(undefined);
    render(
      <NewSessionDialog
        open
        onOpenChange={() => {}}
        onCreate={onCreate}
        models={MODELS}
        defaultWorkdir="   /repo/web   "
      />,
    );
    await user.click(screen.getByTestId("new-session-submit"));
    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1));
    expect(onCreate.mock.calls[0][0]).toMatchObject({ workdir: "/repo/web" });
  });

  it("includes systemPromptPreset in payload only when chosen", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue(undefined);
    render(
      <NewSessionDialog
        open
        onOpenChange={() => {}}
        onCreate={onCreate}
        models={MODELS}
        presets={[
          { value: "engineer", label: "Engineer" },
          { value: "researcher", label: "Researcher" },
        ]}
        defaultWorkdir="/repo"
      />,
    );
    await user.click(screen.getByTestId("new-session-submit"));
    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1));
    const firstPayload = onCreate.mock.calls[0][0];
    expect(firstPayload.systemPromptPreset).toBeUndefined();
  });

  it("calls onOpenChange(false) when Cancel is clicked", () => {
    const onOpenChange = vi.fn();
    render(
      <NewSessionDialog
        open
        onOpenChange={onOpenChange}
        onCreate={() => {}}
        models={MODELS}
      />,
    );
    fireEvent.click(screen.getByTestId("new-session-cancel"));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
