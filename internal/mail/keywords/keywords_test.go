package keywords

import "testing"

func TestValidateAllowsProviderKeywordIDs(t *testing.T) {
	tests := []string{
		"$label2",
		"$VendorFlag",
		"Client",
	}
	for _, input := range tests {
		got, err := Validate(input)
		if err != nil {
			t.Fatalf("Validate(%q) error = %v", input, err)
		}
		if got != input {
			t.Fatalf("Validate(%q) = %q, want same keyword", input, got)
		}
	}
	if _, err := Validate("$Junk"); err == nil {
		t.Fatal("Validate($Junk) error = nil, want status keyword rejection")
	}
}
