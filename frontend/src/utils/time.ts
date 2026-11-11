// "Tue, Nov 10, 02:15 PM" in the viewer's locale.
export function formatInstant(iso: string, withWeekday = false): string {
  return new Date(iso).toLocaleString(undefined, {
    weekday: withWeekday ? 'short' : undefined,
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}
