import { createElement, type ComponentProps } from "react";
import { actionIcon, type ActionId, type ActionState } from "@/lib/itemActionTable";

/** An item action's glyph, chosen by the action table so every surface draws the same one. */
export function ActionIcon({ id, state, ...rest }: { id: ActionId; state?: ActionState } & ComponentProps<"svg">) {
  return createElement(actionIcon(id, state), { "aria-hidden": true, ...rest });
}
