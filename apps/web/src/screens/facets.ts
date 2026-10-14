/** Shared by the page rails' FacetLists: an empty selection matches everything. */
export function inFacet(selected: readonly string[], value: string): boolean {
  return !selected.length || selected.includes(value);
}

/** Each option with how many of `items` have its value: counts cover every item, before filters. */
export function withCounts<O extends { readonly value: string }, T>(options: readonly O[], items: readonly T[], valueOf: (item: T) => string): (O & { count: number })[] {
  return options.map((option) => ({ ...option, count: items.filter((item) => valueOf(item) === option.value).length }));
}
