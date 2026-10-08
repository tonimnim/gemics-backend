/** Countries a competition can be limited to, in launch order. */
export const scopeCountries = [
  'KE',
  'IN',
  'SG',
  'ID',
  'BR',
  'JP',
  'TH',
  'MY',
  'UG',
  'TZ',
  'NG',
] as const

const names = new Intl.DisplayNames(['en'], { type: 'region' })

export const countryName = (code: string) => names.of(code) ?? code

/** The flag emoji for a two-letter country code. */
export const countryFlag = (code: string) =>
  String.fromCodePoint(
    ...code
      .toUpperCase()
      .split('')
      .map((letter) => 0x1f1e6 + letter.charCodeAt(0) - 65)
  )

/** The countries a competition's rules limit entry to; empty means all. */
export const allowedCountries = (rules: unknown): string[] => {
  const list = (rules as { eligibility?: { allowedCountries?: unknown } })
    ?.eligibility?.allowedCountries
  return Array.isArray(list) ? list.filter((c) => typeof c === 'string') : []
}
