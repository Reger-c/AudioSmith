package main

// Country names for the new call form's "Calling …" hint, by the ISO
// region telephony.Region finds (literal i18n.T calls, for the key scan).

import "github.com/hehehai/AudioSmith/internal/i18n"

// callRegionName names a region ("" = unknown).
func callRegionName(region string) string {
	switch region {
	case "EG":
		return i18n.T("Egypt")
	case "ZA":
		return i18n.T("South Africa")
	case "GR":
		return i18n.T("Greece")
	case "NL":
		return i18n.T("the Netherlands")
	case "BE":
		return i18n.T("Belgium")
	case "FR":
		return i18n.T("France")
	case "ES":
		return i18n.T("Spain")
	case "HU":
		return i18n.T("Hungary")
	case "IT":
		return i18n.T("Italy")
	case "RO":
		return i18n.T("Romania")
	case "CH":
		return i18n.T("Switzerland")
	case "AT":
		return i18n.T("Austria")
	case "GB":
		return i18n.T("the United Kingdom")
	case "DK":
		return i18n.T("Denmark")
	case "SE":
		return i18n.T("Sweden")
	case "NO":
		return i18n.T("Norway")
	case "PL":
		return i18n.T("Poland")
	case "DE":
		return i18n.T("Germany")
	case "PE":
		return i18n.T("Peru")
	case "MX":
		return i18n.T("Mexico")
	case "CU":
		return i18n.T("Cuba")
	case "AR":
		return i18n.T("Argentina")
	case "BR":
		return i18n.T("Brazil")
	case "CL":
		return i18n.T("Chile")
	case "CO":
		return i18n.T("Colombia")
	case "VE":
		return i18n.T("Venezuela")
	case "MY":
		return i18n.T("Malaysia")
	case "AU":
		return i18n.T("Australia")
	case "ID":
		return i18n.T("Indonesia")
	case "PH":
		return i18n.T("the Philippines")
	case "NZ":
		return i18n.T("New Zealand")
	case "SG":
		return i18n.T("Singapore")
	case "TH":
		return i18n.T("Thailand")
	case "JP":
		return i18n.T("Japan")
	case "KR":
		return i18n.T("South Korea")
	case "VN":
		return i18n.T("Vietnam")
	case "CN":
		return i18n.T("China")
	case "TR":
		return i18n.T("Türkiye")
	case "IN":
		return i18n.T("India")
	case "PK":
		return i18n.T("Pakistan")
	case "NG":
		return i18n.T("Nigeria")
	case "KE":
		return i18n.T("Kenya")
	case "PT":
		return i18n.T("Portugal")
	case "LU":
		return i18n.T("Luxembourg")
	case "IE":
		return i18n.T("Ireland")
	case "IS":
		return i18n.T("Iceland")
	case "FI":
		return i18n.T("Finland")
	case "UA":
		return i18n.T("Ukraine")
	case "CZ":
		return i18n.T("Czechia")
	case "HK":
		return i18n.T("Hong Kong")
	case "MO":
		return i18n.T("Macao")
	case "TW":
		return i18n.T("Taiwan")
	case "SA":
		return i18n.T("Saudi Arabia")
	case "AE":
		return i18n.T("the United Arab Emirates")
	case "IL":
		return i18n.T("Israel")
	}
	return ""
}
