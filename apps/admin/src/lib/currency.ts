/**
 * Money arrives as integer minor units in its own currency; USD is only the
 * reporting currency. These tables mirror the API's currencies (a Go test
 * checks they agree).
 */

/** ISO 4217 decimal places: amounts are integers of 10^-digits. */
export const minorUnits: Record<string, number> = {
  USD: 2,
  KES: 2,
  INR: 2,
  SGD: 2,
  IDR: 2,
  BRL: 2,
  JPY: 0,
  THB: 2,
  MYR: 2,
  UGX: 0,
  TZS: 2,
  NGN: 2,
}

/** The currency a competition limited to one country is priced in. */
export const countryCurrency: Record<string, string> = {
  US: 'USD',
  KE: 'KES',
  IN: 'INR',
  SG: 'SGD',
  ID: 'IDR',
  BR: 'BRL',
  JP: 'JPY',
  TH: 'THB',
  MY: 'MYR',
  UG: 'UGX',
  TZ: 'TZS',
  NG: 'NGN',
}

/** Entry fees are collected by M-Pesa, so only in KES. */
export const paidEntryCurrency = 'KES'

/** One country's currency, or USD for several countries or all of them. */
export function competitionCurrency(countries: string[]) {
  return (countries.length === 1 && countryCurrency[countries[0]]) || 'USD'
}

const digits = (currency: string) => minorUnits[currency] ?? 2

/** Formats minor units, for example 10050 KES as "KES 100.50". */
export function money(minor: number, currency = 'KES') {
  const places = digits(currency)
  const major = minor / 10 ** places
  return `${currency} ${major.toLocaleString('en-US', {
    minimumFractionDigits: Number.isInteger(major) ? 0 : places,
    maximumFractionDigits: places,
  })}`
}

/** Formats USD cents compactly, for example 123456 as "$1,234.56". */
export function usd(minor: number | null | undefined) {
  if (minor == null) return '—'
  return (minor / 100).toLocaleString('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: minor % 100 === 0 ? 0 : 2,
  })
}

/** Converts an amount typed in major units, like "100.5", to minor units. */
export function toMinor(major: string | number, currency: string) {
  return Math.round(Number(major) * 10 ** digits(currency))
}

/**
 * The USD cents a minor-unit amount is worth at a rate of units per dollar,
 * for showing staff what a price is worth. Finance totals never use this:
 * they use the value frozen when the money moved.
 */
export function toUsdMinor(
  minor: number,
  currency: string,
  unitsPerUsd: string | number | null | undefined
) {
  if (currency === 'USD') return minor
  const rate = Number(unitsPerUsd)
  if (!rate) return null
  return Math.round((minor * 100) / (10 ** digits(currency) * rate))
}
