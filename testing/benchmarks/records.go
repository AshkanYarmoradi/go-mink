package benchmarks

import (
	"fmt"
	"strconv"

	"go-mink.dev/adapters"
)

// SmallEventRecord returns a pre-serialized event record with ~100 bytes of JSON payload.
func SmallEventRecord() adapters.EventRecord {
	return adapters.EventRecord{
		Type: "OrderCreated",
		Data: []byte(`{"orderId":"order-123","customerId":"cust-456","total":99.99}`),
	}
}

// MediumEventRecord returns a pre-serialized event record with ~500 bytes of JSON payload.
func MediumEventRecord() adapters.EventRecord {
	return adapters.EventRecord{
		Type: "OrderUpdated",
		Data: []byte(`{"orderId":"order-123","customerId":"cust-456","total":249.99,"currency":"USD","status":"confirmed","items":[{"sku":"SKU-001","name":"Widget Pro","quantity":2,"price":49.99},{"sku":"SKU-002","name":"Gadget Plus","quantity":1,"price":149.99}],"shippingAddress":{"street":"123 Main St","city":"Springfield","state":"IL","zip":"62701","country":"US"},"billingAddress":{"street":"456 Oak Ave","city":"Chicago","state":"IL","zip":"60601","country":"US"}}`),
	}
}

// MetadataEventRecord returns a pre-serialized event record with rich metadata.
func MetadataEventRecord() adapters.EventRecord {
	return adapters.EventRecord{
		Type: "OrderCreated",
		Data: []byte(`{"orderId":"order-123","customerId":"cust-456","total":99.99}`),
		Metadata: adapters.Metadata{
			CorrelationID: "corr-abc-123-def-456",
			CausationID:   "cmd-create-order-789",
			UserID:        "user-admin-001",
			TenantID:      "tenant-acme-corp",
			Custom: map[string]string{
				"source":          "web-api",
				"$schema_version": "2",
				"request_id":      "req-xyz-789",
			},
		},
	}
}

// MakeBatch creates a batch of EventRecords with the specified size.
// All records share the same "OrderEvent" type with unique JSON payloads.
func MakeBatch(size int) []adapters.EventRecord {
	records := make([]adapters.EventRecord, size)
	for i := range records {
		records[i] = adapters.EventRecord{
			Type: "OrderEvent",
			Data: []byte(fmt.Sprintf(`{"orderId":"order-%d","seq":%d,"total":%.2f}`, i, i, float64(i)*10.5)),
		}
	}
	return records
}

// MakeStreamID creates a unique stream ID for benchmarks using the given index.
func MakeStreamID(prefix string, i int) string {
	return prefix + "-" + strconv.Itoa(i)
}
