import { call, createChannel, each, sleep, spawn, useAbortSignal, } from "effection";
import { spawnFramedWindow } from "./kernel.js";
export const modelList = [
    {
        name: "GPT IV Turbo",
        model: "gpt-4-turbo-preview",
        provider: "openai",
    },
    {
        name: "Claude III Opus",
        model: "claude-3-opus-20240229",
        provider: "anthropic",
    },
    {
        name: "Claude III Sonnet",
        model: "claude-3-sonnet-20240229",
        provider: "anthropic",
    },
    {
        name: "Claude III Haiku",
        model: "claude-3-haiku-20240307",
        provider: "anthropic",
    },
];
export const modelsByName = modelList.reduce((acc, model) => {
    acc[model.name] = model;
    return acc;
}, {});
export function* streamingRequest(apiPath, requestBody) {
    const channel = createChannel();
    const abort = yield* useAbortSignal();
    const response = yield* call(fetch(apiPath, {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
        },
        body: JSON.stringify(requestBody),
        signal: abort,
    }));
    if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
    }
    // Spawn a new operation to handle the streaming response
    yield* spawnFramedWindow("Streaming Response", function* () {
        yield* sleep(1000);
        if (!response.body) {
            throw new Error("No body");
        }
        // Get the reader and decoder for the response body
        const reader = response.body.getReader();
        const decoder = new TextDecoder("utf-8");
        let buffer = "";
        // Loop until the streaming is done
        loop: while (true) {
            // Read the next chunk of data from the response
            const { done, value } = yield* call(reader.read());
            console.log(`[streamingRequest] done: ${done}, value: ${value}`);
            // If the streaming is done, break the loop
            if (done) {
                console.log("Streaming done");
                break;
            }
            // Decode the chunk and append it to the buffer
            buffer += decoder.decode(value);
            let position;
            // Process each line in the buffer
            while ((position = buffer.indexOf("\n")) !== -1) {
                const line = buffer.slice(0, position).trim();
                buffer = buffer.slice(position + 1);
                console.debug(`[streamingRequest] Processing line: ${line}`);
                // Skip empty lines and event lines
                if (line === "") {
                    console.debug(`[streamingRequest] Skipping empty line`);
                    continue;
                }
                if (line.startsWith("event:")) {
                    console.debug(`[streamingRequest] Skipping event line: ${line}`);
                    continue;
                }
                // Extract the message from the line
                const message = line.replace(/^data: /, "");
                console.debug(`[streamingRequest] Extracted message: ${message}`);
                // If the message is "[DONE]", close the channel and break the loop
                if (message === "[DONE]") {
                    console.debug("Received [DONE] message, closing channel");
                    yield* channel.close();
                    break loop;
                }
                console.debug(`[streamingRequest] Sending message through channel: ${message}`);
                // Send the message through the channel
                yield* channel.send(message);
            }
        }
    });
    return channel;
}
export function* stream(model, requestBody) {
    if (model.provider === "openai") {
        return yield* streamOpenAI(model, requestBody);
    }
    else if (model.provider === "anthropic") {
        return yield* streamAnthropic(model, requestBody);
    }
    else {
        throw new Error(`Invalid provider: ${model.provider}`);
    }
}
function* streamOpenAI({ model }, requestBody) {
    const messagesChannel = yield* streamingRequest("https://swash2.less.rest/openai/v1/chat/completions", {
        model,
        temperature: requestBody.temperature,
        max_tokens: requestBody.maxTokens,
        messages: requestBody.messages.map(({ role, content }) => ({
            role,
            content,
        })),
        stream: true,
    });
    const stateChannel = createChannel();
    yield* spawn(function* () {
        let buffer = "";
        for (const message of yield* each(messagesChannel)) {
            const { choices: [{ delta: { content }, finish_reason, },], } = JSON.parse(message);
            if (content && typeof content !== "string") {
                throw new Error(`Invalid content: ${content}`);
            }
            buffer += content;
            yield* stateChannel.send({ role: "assistant", content: buffer });
            if (finish_reason === "stop") {
                yield* stateChannel.close();
                yield* messagesChannel.close();
                break;
            }
            yield* each.next();
        }
    });
    return stateChannel;
}
function* streamAnthropic({ model }, { temperature, maxTokens, systemMessage, messages }) {
    console.log(`[streamAnthropic] Starting stream with model: ${model}, temperature: ${temperature}, maxTokens: ${maxTokens}, systemMessage: ${systemMessage}, messages: ${JSON.stringify(messages)}`);
    const messagesChannel = yield* streamingRequest("https://swash2.less.rest/anthropic/v1/messages", {
        model,
        temperature,
        max_tokens: maxTokens,
        system: systemMessage,
        messages: messages.map(({ role, content }) => ({ role, content })),
        stream: true,
    });
    const stateChannel = createChannel();
    yield* spawn(function* () {
        let content = "";
        loop: for (const message of yield* each(messagesChannel)) {
            const event = JSON.parse(message);
            console.log(`[streamAnthropic] Received event: ${JSON.stringify(event)}`);
            switch (event.type) {
                case "message_start":
                    if (!event.message || !event.message.role) {
                        console.error(`[streamAnthropic] Error: Missing role in message_start event`);
                        throw new Error("Missing role in message_start");
                    }
                    if (event.message.role !== "assistant" &&
                        event.message.role !== "user") {
                        console.error(`[streamAnthropic] Error: Invalid role: ${event.message.role}`);
                        throw new Error(`Invalid role: ${event.message.role}`);
                    }
                    console.log(`[streamAnthropic] Sending message_start state: role=${event.message.role}, content=""`);
                    yield* stateChannel.send({
                        role: event.message.role,
                        content: "",
                    });
                    break;
                case "content_block_delta":
                    if (!event.delta || !event.delta.text) {
                        console.error(`[streamAnthropic] Error: Missing text in content_block_delta event`);
                        throw new Error("Missing text in content_block_delta");
                    }
                    content += event.delta.text;
                    console.log(`[streamAnthropic] Sending content_block_delta state: role="assistant", content="${content}"`);
                    yield* stateChannel.send({
                        role: "assistant",
                        content,
                    });
                    break;
                case "message_stop":
                    console.log(`[streamAnthropic] Received message_stop event, closing channel`);
                    yield* each.next();
                    break loop;
                default:
                    console.warn(`[streamAnthropic] Warning: Unhandled event type: ${event.type}`);
            }
            yield* each.next();
            console.log(`each.next() done`);
        }
        yield* stateChannel.close();
        yield* messagesChannel.close();
    });
    console.log(`[streamAnthropic] Returning stateChannel`);
    return stateChannel;
}
function* app() {
    const channel = yield* stream(modelsByName["Claude III Haiku"], {
        temperature: 1,
        maxTokens: 512,
        systemMessage: "You are a skilled haiku improvisator.",
        messages: [
            { role: "user", content: "Hello!" },
            { role: "assistant", content: "indeed..." },
            {
                role: "user",
                content: "Explain structured concurrency as a series of linked haiku.",
            },
        ],
    });
    for (const { role, content } of yield* each(channel)) {
        console.log(`${role}: ${content}`);
        yield* each.next();
    }
}
export function* extractNounsFromText(text, model) {
    const channel = yield* stream(model, {
        temperature: 0,
        maxTokens: 100,
        systemMessage: "you CHOOSE the most SALIENT words from USER INPUT",
        messages: [
            {
                role: "user",
                content: `<input example>a subject before that was anything a substance and the substance was anything like the flower pot that was stable and had properties</input>`,
            },
            {
                role: "assistant",
                content: "<words>subject, substance, flower pot, stable, properties</words>",
            },
            { role: "user", content: `<input>${text}</input>` },
            { role: "assistant", content: "<words>" },
        ],
    });
    return channel;
}
