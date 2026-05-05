// SPDX-License-Identifier: MIT
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import {
  WorkdirBadge,
  WorkdirPickerDialog,
} from "@/components/workdir/WorkdirPicker";

describe("<WorkdirBadge>", () => {
  it("renders the workdir as a button when present", () => {
    render(<WorkdirBadge workdir="/repo/web" onOpenPicker={() => {}} />);
    const btn = screen.getByTestId("workdir-badge");
    expect(btn.textContent).toContain("/repo/web");
    expect(btn.getAttribute("aria-label")).toBe(
      "Change workdir, current: /repo/web",
    );
  });

  it("renders the no-workdir caption when null", () => {
    render(<WorkdirBadge workdir={null} onOpenPicker={() => {}} />);
    expect(screen.getByTestId("workdir-badge").textContent).toContain(
      "no workdir",
    );
  });

  it("invokes onOpenPicker when clicked", () => {
    const open = vi.fn();
    render(<WorkdirBadge workdir="/repo" onOpenPicker={open} />);
    fireEvent.click(screen.getByTestId("workdir-badge"));
    expect(open).toHaveBeenCalledTimes(1);
  });
});

describe("<WorkdirPickerDialog>", () => {
  it("renders nothing when closed", () => {
    render(
      <WorkdirPickerDialog
        open={false}
        onOpenChange={() => {}}
        onSelect={() => {}}
        listAllowedRoots={() => Promise.resolve([])}
      />,
    );
    expect(screen.queryByTestId("workdir-picker-dialog")).toBeNull();
  });

  it("blocks submit + surfaces error when path is empty", async () => {
    const onSelect = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={onOpenChange}
        onSelect={onSelect}
        defaultPath=""
        listAllowedRoots={() => Promise.resolve([])}
      />,
    );
    fireEvent.click(screen.getByTestId("workdir-picker-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("workdir-picker-error")).toBeTruthy();
    });
    expect(onSelect).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("submits the trimmed path with handle=null on manual entry", async () => {
    const onSelect = vi.fn().mockResolvedValue(undefined);
    const onOpenChange = vi.fn();
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={onOpenChange}
        onSelect={onSelect}
        defaultPath="  /repo/web  "
        listAllowedRoots={() => Promise.resolve([])}
      />,
    );
    fireEvent.click(screen.getByTestId("workdir-picker-submit"));
    await waitFor(() => expect(onSelect).toHaveBeenCalledTimes(1));
    expect(onSelect).toHaveBeenCalledWith("/repo/web", null);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("renders the FSA Choose directory button when showDirectoryPicker is provided", () => {
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={() => {}}
        onSelect={() => {}}
        listAllowedRoots={() => Promise.resolve([])}
        showDirectoryPicker={() =>
          Promise.resolve({ name: "fake" } as FileSystemDirectoryHandle)
        }
      />,
    );
    expect(screen.getByTestId("workdir-picker-fsa")).toBeTruthy();
  });

  it("FSA flow forwards the handle to onSelect", async () => {
    const onSelect = vi.fn().mockResolvedValue(undefined);
    const handle = { name: "web" } as FileSystemDirectoryHandle;
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={() => {}}
        onSelect={onSelect}
        defaultPath="/repo/web"
        listAllowedRoots={() => Promise.resolve([])}
        showDirectoryPicker={() => Promise.resolve(handle)}
      />,
    );
    fireEvent.click(screen.getByTestId("workdir-picker-fsa"));
    await waitFor(() => expect(onSelect).toHaveBeenCalledTimes(1));
    expect(onSelect.mock.calls[0][1]).toBe(handle);
  });

  it("surfaces error when the FSA picker rejects", async () => {
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={() => {}}
        onSelect={() => {}}
        listAllowedRoots={() => Promise.resolve([])}
        showDirectoryPicker={() => Promise.reject(new Error("user cancelled"))}
      />,
    );
    fireEvent.click(screen.getByTestId("workdir-picker-fsa"));
    await waitFor(() => {
      expect(screen.getByTestId("workdir-picker-error")).toBeTruthy();
    });
    expect(screen.getByTestId("workdir-picker-error").textContent).toContain(
      "user cancelled",
    );
  });

  it("populates the autocomplete datalist from listAllowedRoots", async () => {
    render(
      <WorkdirPickerDialog
        open
        onOpenChange={() => {}}
        onSelect={() => {}}
        listAllowedRoots={() => Promise.resolve(["/repo/a", "/repo/b"])}
      />,
    );
    await waitFor(() => {
      const dl = document.getElementById("workdir-picker-roots");
      expect(dl?.children.length).toBe(2);
    });
  });
});
