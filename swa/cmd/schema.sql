CREATE TABLE IF NOT EXISTS media_clips (
    id INTEGER PRIMARY KEY,
    start_time DATETIME,
    end_time DATETIME,
    sample_rate INTEGER
);

CREATE TABLE IF NOT EXISTS opus_frames (
    id INTEGER PRIMARY KEY,
    media_clip_id INTEGER,
    sequence_num INTEGER,
    compressed_audio BLOB,
    FOREIGN KEY (media_clip_id) REFERENCES media_clips (id)
);
