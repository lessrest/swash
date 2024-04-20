import { call } from "effection";
export function* transcribe(blobs, language = "en") {
    const formData = new FormData();
    formData.append("file", new Blob(blobs));
    const response = yield* call(fetch(`/whisper-deepgram?language=${language}`, {
        method: "POST",
        body: formData,
    }));
    if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
    }
    const result = yield* call(response.json());
    console.log(result);
    return result.results.channels[0].alternatives[0];
}
