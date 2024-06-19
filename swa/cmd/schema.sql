CREATE TABLE IF NOT EXISTS opus_streams (
    id INTEGER PRIMARY KEY,
    start_time DATETIME,
    end_time DATETIME
);

CREATE TABLE IF NOT EXISTS opus_frames (
    id INTEGER PRIMARY KEY,
    opus_stream_id INTEGER,
    seq INTEGER,
    bytes BLOB,
    FOREIGN KEY (opus_stream_id) REFERENCES opus_streams (id)
);

