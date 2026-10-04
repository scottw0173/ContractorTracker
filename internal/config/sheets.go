package config

// Sheets contains only the Google integration settings for a future sync worker.
type Sheets struct {
	CredentialsParameter string
	SpreadsheetID        string
}

func LoadSheets() (Sheets, error) {
	parameter, err := required("GOOGLE_CREDENTIALS_PARAMETER")
	if err != nil {
		return Sheets{}, err
	}
	id, err := required("GOOGLE_SPREADSHEET_ID")
	if err != nil {
		return Sheets{}, err
	}
	return Sheets{CredentialsParameter: parameter, SpreadsheetID: id}, nil
}
