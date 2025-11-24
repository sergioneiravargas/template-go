CREATE TABLE queue_outbox (
    id UUID PRIMARY KEY,
    queue_name VARCHAR(255) NOT NULL,
    message JSONB NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    available_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retry_count INT NOT NULL DEFAULT 0,
    last_error TEXT
);

CREATE INDEX idx_queue_outbox_available_at ON queue_outbox(available_at);
CREATE INDEX idx_queue_outbox_queue_name ON queue_outbox(queue_name);
