// SPDX-License-Identifier: MIT
// shadcn/ui canonical cn() helper. Merges class lists and dedupes
// Tailwind utility conflicts (last-wins).
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
