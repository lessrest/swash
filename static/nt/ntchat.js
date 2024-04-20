import { call, createChannel, resource, spawn, useAbortSignal, } from "effection";
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
    return yield* resource(function* (provide) {
        const channel = createChannel();
        const abort = yield* useAbortSignal();
        const subscription = yield* channel;
        yield* spawn(function* () {
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
            if (!response.body) {
                throw new Error("No body");
            }
            const reader = response.body.getReader();
            const decoder = new TextDecoder("utf-8");
            let buffer = "";
            while (true) {
                const { done, value } = yield* call(reader.read());
                if (done) {
                    console.log("Streaming done");
                    break;
                }
                buffer += decoder.decode(value);
                let position;
                while ((position = buffer.indexOf("\n")) !== -1) {
                    const line = buffer.slice(0, position).trim();
                    buffer = buffer.slice(position + 1);
                    if (line === "") {
                        continue;
                    }
                    if (line.startsWith("event:")) {
                        continue;
                    }
                    // Extract the message from the line
                    const message = line.replace(/^data: /, "");
                    // If the message is "[DONE]", close the channel and break the loop
                    if (message === "[DONE]") {
                        console.debug("Received [DONE] message, closing channel");
                        yield* channel.close();
                        return;
                    }
                    console.debug(`[streamingRequest] Sending message through channel: ${message}`);
                    // Send the message through the channel
                    yield* channel.send(message);
                }
            }
        });
        yield* provide(subscription);
    });
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
    return yield* resource(function* (provide) {
        const messageSubscription = yield* streamingRequest("https://swash2.less.rest/openai/v1/chat/completions", {
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
        const stateSubscription = yield* stateChannel;
        yield* spawn(function* () {
            let buffer = "";
            let next = yield* messageSubscription.next();
            while (!next.done && next.value) {
                const { choices: [{ delta: { content }, finish_reason, },], } = JSON.parse(next.value);
                if (content && typeof content !== "string") {
                    throw new Error(`Invalid content: ${content}`);
                }
                buffer += content;
                yield* stateChannel.send({ role: "assistant", content: buffer });
                if (finish_reason === "stop") {
                    yield* stateChannel.close();
                    break;
                }
                next = yield* messageSubscription.next();
            }
        });
        yield* provide(stateSubscription);
    });
}
function* streamAnthropic({ model }, { temperature, maxTokens, systemMessage, messages }) {
    console.log(`[streamAnthropic] Starting stream with model: ${model}, temperature: ${temperature}, maxTokens: ${maxTokens}, systemMessage: ${systemMessage}, messages: ${JSON.stringify(messages)}`);
    const messagesSubscription = yield* streamingRequest("https://swash2.less.rest/anthropic/v1/messages", {
        model,
        temperature,
        max_tokens: maxTokens,
        system: systemMessage,
        messages: messages.map(({ role, content }) => ({ role, content })),
        stream: true,
    });
    return yield* resource(function* (provide) {
        const stateChannel = createChannel();
        const stateSubscription = yield* stateChannel;
        yield* spawn(function* () {
            let content = "";
            let next = yield* messagesSubscription.next();
            try {
                while (!next.done && next.value) {
                    const event = JSON.parse(next.value);
                    console.log(`[streamAnthropic] Received event: ${JSON.stringify(event)}`);
                    console.info(event.type, event);
                    try {
                        switch (event.type) {
                            case "content_block_delta":
                                if (!event.delta || !event.delta.text) {
                                    throw new Error("Missing text in content_block_delta");
                                }
                                content += event.delta.text;
                                console.log(`[streamAnthropic] Sending content_block_delta state: role="assistant", content="${content}"`);
                                yield* stateChannel.send({
                                    role: "assistant",
                                    content: event.delta.text,
                                });
                                break;
                            case "message_stop":
                                console.log(`[streamAnthropic] Received message_stop event, closing channel`);
                                return;
                            default:
                                console.warn(`[streamAnthropic] Warning: Unhandled event type: ${event.type}`);
                        }
                    }
                    catch (e) {
                        console.error(`[streamAnthropic] Error: ${e}`);
                    }
                    next = yield* messagesSubscription.next();
                }
            }
            finally {
                yield* stateChannel.close();
            }
        });
        yield* provide(stateSubscription);
    });
}
// function* app() {
//   const channel = yield* stream(modelsByName["Claude III Haiku"], {
//     temperature: 1,
//     maxTokens: 512,
//     systemMessage: "You are a skilled haiku improvisator.",
//     messages: [
//       { role: "user", content: "Hello!" },
//       { role: "assistant", content: "indeed..." },
//       {
//         role: "user",
//         content:
//           "Explain structured concurrency as a series of linked haiku.",
//       },
//     ],
//   })
//   for (const { role, content } of yield* each(channel)) {
//     console.log(`${role}: ${content}`)
//     yield* each.next()
//   }
// }
// export function* extractNounsFromText(
//   text: string,
//   model: Model,
// ): Operation<Channel<MessageState, void>> {
//   const channel = yield* stream(model, {
//     temperature: 0,
//     maxTokens: 100,
//     systemMessage: "you CHOOSE the most SALIENT words from USER INPUT",
//     messages: [
//       {
//         role: "user",
//         content: `<input example>a subject before that was anything a substance and the substance was anything like the flower pot that was stable and had properties</input>`,
//       },
//       {
//         role: "assistant",
//         content:
//           "<words>subject, substance, flower pot, stable, properties</words>",
//       },
//       { role: "user", content: `<input>${text}</input>` },
//       { role: "assistant", content: "<words>" },
//     ],
//   })
//   return channel
// }
