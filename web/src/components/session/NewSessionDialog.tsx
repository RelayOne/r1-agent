// SPDX-License-Identifier: MIT
// <NewSessionDialog> — modal form for r1d.session.create. Spec item 24/55.
//
// Validates inputs against `CreateSessionRequestSchema` (`@/lib/api/types`)
// via `react-hook-form` + `zodResolver`. The dialog stays in "open" or
// "closed" state via the `open`/`onOpenChange` pair (controlled by the
// caller). On valid submit it calls `onCreate(payload)` and closes;
// caller is responsible for issuing the actual r1d RPC.
//
// Form fields:
//   - model (select):       required, default = first option in `models`.
//   - workdir (text input): required, must be a non-empty string.
//   - systemPromptPreset (select): optional, default = "" (omitted from
//                          the payload when empty).
import { useEffect } from "react";
import type { ReactElement } from "react";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { CreateSessionRequest } from "@/lib/api/types";

const FormSchema = z.object({
  model: z.string().min(1, "Model is required"),
  workdir: z
    .string()
    .min(1, "Workdir is required")
    .refine((v) => v.trim().length > 0, "Workdir is required"),
  systemPromptPreset: z.string(),
});

type FormValues = z.infer<typeof FormSchema>;

export interface NewSessionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Callback invoked with a validated CreateSessionRequest payload. */
  onCreate: (payload: CreateSessionRequest) => Promise<void> | void;
  /** Available model identifiers; first entry is the default selection. */
  models: ReadonlyArray<{ value: string; label: string }>;
  /** Available preset names; empty array hides the preset row. */
  presets?: ReadonlyArray<{ value: string; label: string }>;
  /** Pre-filled workdir (e.g. last-used). */
  defaultWorkdir?: string;
}

const NO_PRESET_VALUE = "__none__";

export function NewSessionDialog({
  open,
  onOpenChange,
  onCreate,
  models,
  presets = [],
  defaultWorkdir = "",
}: NewSessionDialogProps): ReactElement {
  const defaultModel = models[0]?.value ?? "";

  const {
    control,
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(FormSchema),
    defaultValues: {
      model: defaultModel,
      workdir: defaultWorkdir,
      systemPromptPreset: NO_PRESET_VALUE,
    },
  });

  // When the dialog re-opens, reset the form. Avoids stale values
  // bleeding across opens after a successful submit.
  useEffect(() => {
    if (open) {
      reset({
        model: defaultModel,
        workdir: defaultWorkdir,
        systemPromptPreset: NO_PRESET_VALUE,
      });
    }
  }, [open, defaultModel, defaultWorkdir, reset]);

  const onSubmit = async (values: FormValues): Promise<void> => {
    const payload: CreateSessionRequest = {
      model: values.model,
      workdir: values.workdir.trim(),
      ...(values.systemPromptPreset && values.systemPromptPreset !== NO_PRESET_VALUE
        ? { systemPromptPreset: values.systemPromptPreset }
        : {}),
    };
    await onCreate(payload);
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        data-testid="new-session-dialog"
        aria-label="New session"
      >
        <DialogHeader>
          <DialogTitle>New session</DialogTitle>
          <DialogDescription>
            Pick a model and pin a workdir. The session opens in a new chat
            once it&apos;s registered with the daemon.
          </DialogDescription>
        </DialogHeader>

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-4"
          data-testid="new-session-form"
          aria-label="New session form"
        >
          <div className="space-y-1">
            <label
              htmlFor="new-session-model"
              className="text-sm font-medium"
            >
              Model
            </label>
            <Controller
              name="model"
              control={control}
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger
                    id="new-session-model"
                    data-testid="new-session-model"
                    aria-label="Model"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {models.map((m) => (
                      <SelectItem
                        key={m.value}
                        value={m.value}
                        data-testid={`new-session-model-${m.value}`}
                      >
                        {m.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            {errors.model ? (
              <p
                role="alert"
                data-testid="new-session-model-error"
                className="text-xs text-destructive"
              >
                {errors.model.message}
              </p>
            ) : null}
          </div>

          <div className="space-y-1">
            <label
              htmlFor="new-session-workdir"
              className="text-sm font-medium"
            >
              Workdir
            </label>
            <Input
              id="new-session-workdir"
              type="text"
              autoComplete="off"
              spellCheck={false}
              data-testid="new-session-workdir"
              aria-label="Workdir"
              aria-invalid={errors.workdir ? "true" : "false"}
              {...register("workdir")}
            />
            {errors.workdir ? (
              <p
                role="alert"
                data-testid="new-session-workdir-error"
                className="text-xs text-destructive"
              >
                {errors.workdir.message}
              </p>
            ) : null}
          </div>

          {presets.length > 0 ? (
            <div className="space-y-1">
              <label
                htmlFor="new-session-preset"
                className="text-sm font-medium"
              >
                System-prompt preset (optional)
              </label>
              <Controller
                name="systemPromptPreset"
                control={control}
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger
                      id="new-session-preset"
                      data-testid="new-session-preset"
                      aria-label="System-prompt preset"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem
                        value={NO_PRESET_VALUE}
                        data-testid={`new-session-preset-${NO_PRESET_VALUE}`}
                      >
                        No preset
                      </SelectItem>
                      {presets.map((p) => (
                        <SelectItem
                          key={p.value}
                          value={p.value}
                          data-testid={`new-session-preset-${p.value}`}
                        >
                          {p.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </div>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              data-testid="new-session-cancel"
              aria-label="Cancel new session"
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="new-session-submit"
              aria-label="Create session"
              disabled={isSubmitting}
            >
              {isSubmitting ? "Creating…" : "Create session"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
