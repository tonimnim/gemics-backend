package httpapi

import "testing"

func TestNormalizeKenyanPhone(t *testing.T) {
	for _, input := range []string{"0712 345 678", "+254712345678", "254112345678"} {
		value, ok := normalizeKenyanPhone(input)
		if !ok || len(value) != 12 {
			t.Fatalf("expected %q to normalize, got %q %v", input, value, ok)
		}
	}
	for _, input := range []string{"", "254212345678", "07123"} {
		if _, ok := normalizeKenyanPhone(input); ok {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestNumericMPesaCallbackMetadataPreservesDigits(t *testing.T) {
	raw := []byte(`{"Body":{"stkCallback":{"MerchantRequestID":"merchant-1","CheckoutRequestID":"checkout-1","ResultCode":0,"ResultDesc":"Success","CallbackMetadata":{"Item":[{"Name":"Amount","Value":250.00},{"Name":"MpesaReceiptNumber","Value":"ABC123"},{"Name":"TransactionDate","Value":20260809112233},{"Name":"PhoneNumber","Value":254712345678}]}}}}`)
	var callback callbackEnvelope
	if err := decodeMPesaJSON(raw, &callback); err != nil {
		t.Fatal(err)
	}
	metadata := callbackMetadata(callback.Body.STKCallback.CallbackMetadata.Items)
	if metadata["PhoneNumber"] != "254712345678" || metadata["TransactionDate"] != "20260809112233" || metadata["Amount"] != "250.00" {
		t.Fatalf("numeric metadata lost precision: %+v", metadata)
	}
	minor, err := parseKESMinor(metadata["Amount"])
	if err != nil || minor != 25_000 {
		t.Fatalf("unexpected parsed amount: %d %v", minor, err)
	}
}

func TestPaymentQueryBackoffIsBounded(t *testing.T) {
	if paymentQueryBackoff(0).Seconds() != 10 || paymentQueryBackoff(100).Minutes() != 5 {
		t.Fatal("unexpected query backoff bounds")
	}
}
