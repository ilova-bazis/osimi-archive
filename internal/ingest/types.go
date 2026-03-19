package ingest

type EnqueuePayload struct {
	BatchPath       string `json:"batch_path"`
	IngestionID     string `json:"ingestion_id,omitempty"`
	IngestionItemID string `json:"ingestion_item_id,omitempty"`
}
