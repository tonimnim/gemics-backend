import { format, formatDistanceToNowStrict } from 'date-fns'

export function money(minor: number, currency = 'KES') {
  return `${currency} ${(minor / 100).toLocaleString('en-KE', {
    minimumFractionDigits: minor % 100 === 0 ? 0 : 2,
  })}`
}

export function dateTime(value: string | null | undefined) {
  return value ? format(new Date(value), 'd MMM yyyy, HH:mm') : '—'
}

export function ago(value: string | null | undefined) {
  return value ? `${formatDistanceToNowStrict(new Date(value))} ago` : '—'
}

export function shortId(id: string | null | undefined) {
  return id ? id.slice(0, 8) : '—'
}

export function initials(name: string) {
  return name
    .split(/[\s_.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]!.toUpperCase())
    .join('')
}

export function humanize(value: string) {
  return value
    .replace(/_/g, ' ')
    .replace(/^./, (char: string) => char.toUpperCase())
}
