package httpapi

import (
	"net/http"
	"slices"
	"sort"
)

// Money is always stored in the currency it is priced or paid in, as an
// integer count of minor units; USD is only the reporting currency (see
// fx_rates.go). These tables mirror the currencies migration seed.

// reportingCurrency is what finance totals are shown in.
const reportingCurrency = "USD"

type currencyInfo struct {
	Code string `json:"code"`
	// MinorUnit is the ISO 4217 exponent: amounts are integers of 10^-MinorUnit.
	MinorUnit int      `json:"minorUnit"`
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
}

var supportedCurrencies = map[string]currencyInfo{
	"USD": {Code: "USD", MinorUnit: 2, Name: "US dollar"},
	"KES": {Code: "KES", MinorUnit: 2, Name: "Kenyan shilling"},
	"INR": {Code: "INR", MinorUnit: 2, Name: "Indian rupee"},
	"SGD": {Code: "SGD", MinorUnit: 2, Name: "Singapore dollar"},
	"IDR": {Code: "IDR", MinorUnit: 2, Name: "Indonesian rupiah"},
	"BRL": {Code: "BRL", MinorUnit: 2, Name: "Brazilian real"},
	"JPY": {Code: "JPY", MinorUnit: 0, Name: "Japanese yen"},
	"THB": {Code: "THB", MinorUnit: 2, Name: "Thai baht"},
	"MYR": {Code: "MYR", MinorUnit: 2, Name: "Malaysian ringgit"},
	"UGX": {Code: "UGX", MinorUnit: 0, Name: "Ugandan shilling"},
	"TZS": {Code: "TZS", MinorUnit: 2, Name: "Tanzanian shilling"},
	"NGN": {Code: "NGN", MinorUnit: 2, Name: "Nigerian naira"},
}

// countryCurrencies is the currency a competition limited to one country is
// priced in.
var countryCurrencies = map[string]string{
	"US": "USD", "KE": "KES", "IN": "INR", "SG": "SGD", "ID": "IDR", "BR": "BRL",
	"JP": "JPY", "TH": "THB", "MY": "MYR", "UG": "UGX", "TZ": "TZS", "NG": "NGN",
}

// paidEntryCurrency is the only currency entry fees can be collected in:
// M-Pesa, which serves Kenya.
const paidEntryCurrency = "KES"

// competitionCurrencyFor is the currency a competition is priced in. A
// competition open to exactly one country with a supported currency uses that
// currency, so players see local prices; any other competition, including one
// open to every country, uses USD.
func competitionCurrencyFor(policy competitionEligibilityRules) string {
	if len(policy.AllowedCountries) == 1 {
		if currency, ok := countryCurrencies[policy.AllowedCountries[0]]; ok {
			return currency
		}
	}
	return reportingCurrency
}

// minorUnitsPerMajor is 10^exponent for a supported currency.
func minorUnitsPerMajor(currency string) int64 {
	units := int64(1)
	for range supportedCurrencies[currency].MinorUnit {
		units *= 10
	}
	return units
}

// currencyList is every supported currency with the countries priced in it,
// in a stable order.
func currencyList() []currencyInfo {
	countries := map[string][]string{}
	for country, currency := range countryCurrencies {
		countries[currency] = append(countries[currency], country)
	}
	result := make([]currencyInfo, 0, len(supportedCurrencies))
	for _, info := range supportedCurrencies {
		info.Countries = countries[info.Code]
		slices.Sort(info.Countries)
		if info.Countries == nil {
			info.Countries = []string{}
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

func (s *Server) registerCurrencyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/currencies", s.listCurrencies)
}

// listCurrencies tells clients how to format each currency's minor units and
// which country uses it. It changes only with a release.
func (s *Server) listCurrencies(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"data": currencyList(), "reportingCurrency": reportingCurrency, "paidEntryCurrency": paidEntryCurrency,
	})
}
