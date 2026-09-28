// Golden values for internal/store/sqlstore TestDateFns. Run inside a Kutt checkout:
//   TZ=UTC node datefns_golden.js
const { differenceInHours, differenceInDays, differenceInMonths } = require("date-fns");
const cases = [
  ["2026-03-31T12:30:00Z", "2026-03-31T12:00:00Z"],
  ["2026-03-31T12:30:00Z", "2026-03-30T12:30:00.001Z"],
  ["2026-03-31T12:30:00Z", "2026-03-28T13:00:00Z"],
  ["2026-03-31T12:30:00Z", "2026-02-28T12:30:00Z"],
  ["2026-03-31T12:30:00Z", "2026-02-28T13:30:00Z"],
  ["2026-02-28T00:00:00Z", "2026-01-31T00:00:00Z"],
  ["2026-03-30T00:00:00Z", "2026-01-31T00:00:00Z"],
  ["2026-03-31T12:30:00Z", "2025-03-31T12:30:01Z"],
  ["2024-02-29T10:00:00Z", "2023-11-30T11:00:00Z"],
];
for (const [l, r] of cases) {
  const a = new Date(l), b = new Date(r);
  console.log(l, r, differenceInHours(a, b), differenceInDays(a, b), differenceInMonths(a, b));
}
