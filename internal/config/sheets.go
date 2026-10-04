package config

// Sheets contains the manual projection worker settings.
type Sheets struct {
	TableName            string
	CredentialsParameter string
	SpreadsheetID        string
}

func LoadSheets() (Sheets, error) {
	table, err := required("TABLE_NAME")
	if err != nil {
		return Sheets{}, err
	}
	parameter, err := required("GOOGLE_CREDENTIALS_PARAMETER")
	if err != nil {
		return Sheets{}, err
	}
	id, err := required("GOOGLE_SPREADSHEET_ID")
	if err != nil {
		return Sheets{}, err
	}
	return Sheets{TableName: table, CredentialsParameter: parameter, SpreadsheetID: id}, nil
}
