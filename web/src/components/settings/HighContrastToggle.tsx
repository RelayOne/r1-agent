// SPDX-License-Identifier: MIT
// <HighContrastToggle> — flips the active theme between user's prior
// theme and the high-contrast theme. Spec item 39/55 (one surface).
//
// We track the user's previous non-hc choice in component state so
// turning hc off restores their original mode (light/dark/system),
// not always defaulting to "system".
import { useState } from "react";
import type { ReactElement } from "react";
import { useTheme } from "@/components/layout/ThemeProvider";
import type { ThemeMode } from "@/components/layout/ThemeProvider";
import { Button } from "@/components/ui/button";
import { Eye } from "lucide-react";

export function HighContrastToggle(): ReactElement {
  const { theme, setTheme } = useTheme();
  const [previous, setPrevious] = useState<ThemeMode>(
    theme === "hc" ? "system" : theme,
  );
  const isHc = theme === "hc";

  const onToggle = (): void => {
    if (isHc) {
      setTheme(previous);
    } else {
      setPrevious(theme);
      setTheme("hc");
    }
  };

  return (
    <Button
      type="button"
      variant={isHc ? "secondary" : "ghost"}
      size="sm"
      onClick={onToggle}
      data-testid="high-contrast-toggle"
      aria-pressed={isHc}
      aria-label={isHc ? "Disable high contrast" : "Enable high contrast"}
    >
      <Eye className="w-3 h-3 mr-1" aria-hidden="true" />
      {isHc ? "HC on" : "HC off"}
    </Button>
  );
}
