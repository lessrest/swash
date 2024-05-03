import { tag, asyncTag } from "./tag.ts"
import { TelegramService } from "./telegram-service.ts"

export class TelegramClientView extends HTMLElement {
  connectedCallback() {
    this.chats = document.createElement("div")
    this.chats.id = "chats"
    this.appendChild(this.chats)

    this.service = new TelegramService()
    this.service.subscribe((update) => this.handleUpdate(update))
    this.service.start()
  }

  /**
   * @param {AnyUpdate} update - The update to handle.
   */
  handleUpdate(update) {
    console.log(update)

    if (update["@type"] === "updateNewChat") {
      this.chats.appendChild(this.renderChat(update.chat))
    } else if (update["@type"] === "updateChatPosition") {
      const chat = document.getElementById(`chat:${update.chat_id}`)
      chat.remove()
      const { list, order } = update.position
      if (list["@type"] === "chatListMain") {
        // find the chat's proper position by going through and looking at the order
        const chats = this.chats
        const children = Array.from(chats.children)
        let i = 0
        for (const child of children) {
          if (parseInt(child.getAttribute("data-order")) < order) {
            break
          }
          i++
        }
        chats.insertBefore(chat, children[i])
        chat.setAttribute("data-order", order.toString())
      }
    } else if (update["@type"] === "updateChatLastMessage") {
      if (!update.last_message) return
      const chat = document.getElementById(`chat:${update.chat_id}`)
      const lastMessage = chat.querySelector(".last-message")
      lastMessage.innerHTML = ""
      lastMessage.appendChild(this.renderMessage(update.last_message))
    } else if (update["@type"] === "updateNewMessage") {
      const chat = document.getElementById(`chat:${update.message.chat_id}`)
      const lastMessage = chat.querySelector(".messages")
      lastMessage.appendChild(this.renderMessage(update.message))
    }
  }

  /**
   * @param {Chat} chat
   */
  renderChat(chat) {
    const permissionsText = chat.permissions
      ? "Permissions: " +
        Object.keys(chat.permissions)
          .filter((key) => chat.permissions[key])
          .join(", ")
      : ""

    const card = tag(
      "div",
      {
        "class": "chat",
        "id": `chat:${chat.id}`,
        "data-order": 0,
      },
      [
        tag("div", { class: "title" }, [chat.title]),
        tag("div", { class: "permissions" }, [permissionsText]),
        tag("div", { class: "messages" }, []),
        tag("div", { class: "last-message" }, []),
      ],
    )

    card.ondblclick = () => {
      console.log(chat)
    }

    return card
  }

  /**
   * @param {Message} message
   */
  renderMessage(message) {
    return tag("div", { class: "message" }, [
      [this.renderSender(message.sender_id)],
      message.content ? this.renderMessageContent(message.content) : null,
    ])
  }

  /**
   * @param {MessageSender} sender
   */
  renderSender(sender) {
    if (sender["@type"] === "messageSenderUser") {
      return tag("div", { class: "user sender" }, [
        this.renderUserSender(sender.user_id),
      ])
    } else if (sender["@type"] === "messageSenderChat") {
      return tag("div", { class: "chat sender" }, [
        this.renderChatSender(sender.chat_id),
      ])
    } else {
      return tag("div", { class: "unknown sender" }, ["Unknown sender"])
    }
  }

  /**
   * @param {number} userId
   */
  renderUserSender(userId) {
    return asyncTag(
      this.service
        .send({ "@type": "getUser", "user_id": userId })
        .then((user) =>
          tag("span", { class: "user name" }, [user.first_name]),
        ),
    )
  }

  /**
   * @param {number} chatId
   */
  renderChatSender(chatId) {
    return asyncTag(
      this.service
        .send({ "@type": "getChat", "chat_id": chatId })
        .then((chat) => tag("span", { class: "chat title" }, [chat.title])),
    )
  }

  /**
   * @param {MessageContent} content
   */
  renderMessageContent(content) {
    if (content["@type"] === "messageText") {
      return tag("div", { class: "text content" }, [content.text.text])
    } else {
      return tag("div", { class: "unknown content" }, [content["@type"]])
    }
  }
}

customElements.define("telegram-client", TelegramClientView)
