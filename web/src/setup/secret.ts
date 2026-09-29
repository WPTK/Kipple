import { createStore } from "@/lib/store";

/**
 * The password typed in step 2, kept only in this page's memory (never stored anywhere) so that step 7 can create the
 * API password without asking for it again. A reload forgets it; step 7 then asks, as Settings does.
 */
export const setupSecret = createStore<string | null>(null);
