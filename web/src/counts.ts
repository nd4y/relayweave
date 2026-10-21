const pluralRules = new Intl.PluralRules("ru");

export function countRu(value: number, one: string, few: string, many: string) {
  const count = Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0;
  const category = pluralRules.select(count);
  return `${count} ${category === "one" ? one : category === "few" ? few : many}`;
}
