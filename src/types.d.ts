interface Update {
  "@type": string
}

type AnyUpdate =
  | UpdateAuthorizationState
  | UpdateOption
  | UpdateNewMessage
  | UpdateMessageEdited
  | UpdateNewChat
  | UpdateChatPosition
  | UpdateChatLastMessage

interface UpdateAuthorizationState extends Update {
  "@type": "updateAuthorizationState"
  "authorization_state": AuthorizationState
}

type AuthorizationState =
  | { "@type": "authorizationStateWaitTdlibParameters" }
  | { "@type": "authorizationStateWaitEncryptionKey" }
  | { "@type": "authorizationStateWaitPhoneNumber" }
  | { "@type": "authorizationStateWaitCode" }
  | { "@type": "authorizationStateWaitPassword" }
  | { "@type": "authorizationStateReady" }
  | { "@type": "authorizationStateLoggingOut" }
  | { "@type": "authorizationStateClosing" }
  | { "@type": "authorizationStateClosed" }

interface UpdateOption extends Update {
  "@type": "updateOption"
  "name": string
  "value": OptionValue
}

type OptionValue =
  | OptionValueBoolean
  | OptionValueEmpty
  | OptionValueInteger
  | OptionValueString

interface OptionValueBoolean {
  "@type": "optionValueBoolean"
  "value": boolean
}

interface OptionValueEmpty {
  "@type": "optionValueEmpty"
}

interface OptionValueInteger {
  "@type": "optionValueInteger"
  "value": number
}

interface OptionValueString {
  "@type": "optionValueString"
  "value": string
}

interface UpdateNewMessage extends Update {
  "@type": "updateNewMessage"
  "message": Message
}

interface UpdateMessageEdited extends Update {
  "@type": "updateMessageEdited"
  "messageId": number
  "editDate": number
}

interface UpdateNewChat extends Update {
  "@type": "updateNewChat"
  "chat": Chat
}

interface UpdateChatLastMessage extends Update {
  "@type": "updateChatLastMessage"
  "chat_id": number
  "last_message": Message
  "positions": ChatPosition[] | null
}

interface UpdateChatPosition extends Update {
  "@type": "updateChatPosition"
  "chat_id": number
  "position": ChatPosition
}

interface Chat {
  "@type": "chat"
  "id": number
  "type": ChatType
  "title": string
  "photo": ChatPhotoInfo | null
  "permissions": ChatPermissions | null
  "last_message": Message | null
  "positions": ChatPosition[] | null
  "is_pinned": boolean
  "is_marked_as_unread": boolean
  "is_sponsored": boolean
  "can_be_deleted_only_for_self": boolean
  "can_be_deleted_for_all_users": boolean
  "can_be_reported": boolean
  "default_disable_notification": boolean
  "unread_count": number
  "last_read_inbox_message_id": number
  "last_read_outbox_message_id": number
  "unread_mention_count": number
  "notification_settings": ChatNotificationSettings | null
  "pinned_message_id": number
  "reply_markup_message_id": number
  "draft_message": DraftMessage | null
  "client_data": string
}

interface ChatPosition {
  "@type": "chatPosition"
  "list": ChatList
  "order": bigint
  "is_pinned": boolean
}

interface ChatPermissions {
  "@type": "chatPermissions"
  "can_send_messages": boolean
  "can_send_media_messages": boolean
  "can_send_polls": boolean
  "can_send_other_messages": boolean
  "can_add_web_page_previews": boolean
  "can_change_info": boolean
  "can_invite_users": boolean
  "can_pin_messages": boolean
}

interface MessageSenderUser {
  "@type": "messageSenderUser"
  "user_id": number
}

interface MessageSenderChat {
  "@type": "messageSenderChat"
  "chat_id": number
}

type MessageSender = MessageSenderUser | MessageSenderChat

interface MessageSendingState {}

type MessageSchedulingState =
  | MessageSchedulingStateSendAtDate
  | MessageSchedulingStateSendWhenOnline

interface MessageSchedulingStateSendAtDate {
  "@type": "messageSchedulingStateSendAtDate"
  "send_date": number
}

interface MessageSchedulingStateSendWhenOnline {
  "@type": "messageSchedulingStateSendWhenOnline"
}

interface MessageForwardInfo {
  "@type": "messageForwardInfo"
  "origin": MessageForwardOrigin
  "date": number
  "public_service_announcement_type": string
  "from_chat_id": number
  "from_message_id": number
}

type MessageForwardOrigin =
  | MessageForwardOriginUser
  | MessageForwardOriginChat
  | MessageForwardOriginHiddenUser
  | MessageForwardOriginChannel
  | MessageForwardOriginMessageImport

interface MessageForwardOriginUser {
  "@type": "messageForwardOriginUser"
  "sender_user_id": number
}

interface MessageForwardOriginChat {
  "@type": "messageForwardOriginChat"
  "sender_chat_id": number
  "author_signature": string
}

interface MessageForwardOriginHiddenUser {
  "@type": "messageForwardOriginHiddenUser"
  "sender_name": string
}

interface MessageForwardOriginChannel {
  "@type": "messageForwardOriginChannel"
  "chat_id": number
  "message_id": number
  "author_signature": string
}

interface MessageForwardOriginMessageImport {
  "@type": "messageForwardOriginMessageImport"
  "sender_name": string
}

interface MessageInteractionInfo {
  "@type": "messageInteractionInfo"
  "view_count": number
  "forward_count": number
  "reply_info": MessageReplyInfo | null
}

interface MessageReplyInfo {
  "@type": "messageReplyInfo"
  "reply_count": number
  "recent_repliers": MessageSender[]
  "last_read_inbox_message_id": number
  "last_read_outbox_message_id": number
  "last_message_id": number
}

// Inherited by messageAnimatedEmoji, messageAnimation, messageAudio, messageBasicGroupChatCreate, messageCall, messageChatAddMembers, messageChatChangePhoto, messageChatChangeTitle, messageChatDeleteMember, messageChatDeletePhoto, messageChatJoinByLink, messageChatJoinByRequest, messageChatSetTheme, messageChatSetTtl, messageChatUpgradeFrom, messageChatUpgradeTo, messageContact, messageContactRegistered, messageCustomServiceAction, messageDice, messageDocument, messageExpiredPhoto, messageExpiredVideo, messageGame, messageGameScore, messageInviteVideoChatParticipants, messageInvoice, messageLocation, messagePassportDataReceived, messagePassportDataSent, messagePaymentSuccessful, messagePaymentSuccessfulBot, messagePhoto, messagePinMessage, messagePoll, messageProximityAlertTriggered, messageScreenshotTaken, messageSticker, messageSupergroupChatCreate, messageText, messageUnsupported, messageVenue, messageVideo, messageVideoChatEnded, messageVideoChatScheduled, messageVideoChatStarted, messageVideoNote, messageVoiceNote, and messageWebsiteConnected.
type MessageContent =
  //  | MessageContentAnimatedEmoji
  //  | MessageContentAnimation
  //  | MessageContentAudio
  //  | MessageContentBasicGroupChatCreate
  //  | MessageContentCall
  //  | MessageContentChatAddMembers
  //  | MessageContentChatChangePhoto
  //  | MessageContentChatChangeTitle
  //  | MessageContentChatDeleteMember
  //  | MessageContentChatDeletePhoto
  //  | MessageContentChatJoinByLink
  //  | MessageContentChatJoinByRequest
  //  | MessageContentChatSetTheme
  //  | MessageContentChatSetTtl
  // | MessageContentChatUpgradeFrom
  // | MessageContentChatUpgradeTo
  // | MessageContentContact
  // | MessageContentContactRegistered
  // | MessageContentCustomServiceAction
  // | MessageContentDice
  //  | MessageContentDocument
  // | MessageContentExpiredPhoto
  // | MessageContentExpiredVideo
  // | MessageContentGame
  // | MessageContentGameScore
  // | MessageContentInviteVideoChatParticipants
  // | MessageContentInvoice
  //  | MessageContentLocation
  // | MessageContentPassportDataReceived
  // | MessageContentPassportDataSent
  // | MessageContentPaymentSuccessful
  // | MessageContentPaymentSuccessfulBot
  //  | MessageContentPhoto
  //  | MessageContentPinMessage
  // | MessageContentPoll
  // | MessageContentProximityAlertTriggered
  // | MessageContentScreenshotTaken
  //  | MessageContentSticker
  // | MessageContentSupergroupChatCreate
  MessageContentText
// | MessageContentUnsupported
// | MessageContentVenue
//  | MessageContentVideo
//  | MessageContentVideoChatEnded
// | MessageContentVideoChatScheduled
//  | MessageContentVideoChatStarted
//  | MessageContentVideoNote
//  | MessageContentVoiceNote
// | MessageContentWebsiteConnected

interface MessageContentAnimatedEmoji {
  "@type": "messageAnimatedEmoji"
  "emoji": string
  "animated_emoji": AnimatedEmoji
}

interface MessageContentAudio {
  "@type": "messageAudio"
  "audio": Audio
  "caption": FormattedText
}

interface MessageContentDocument {
  "@type": "messageDocument"
  "document": Document
  "caption": FormattedText
}

interface MessageContentLocation {
  "@type": "messageLocation"
  "location": TelegramLocation
  "live_period": number
  "expires_in": number
  "heading": number
}

interface TelegramLocation {
  "@type": "location"
  "latitude": number
  "longitude": number
  "horizontal_accuracy": number
}

interface MessageContentPhoto {
  "@type": "messagePhoto"
  "photo": Photo
  "caption": FormattedText
  "is_secret": boolean
}

interface MessageContentPinMessage {
  "@type": "messagePinMessage"
  "message_id": number
}

interface MessageContentSticker {
  "@type": "messageSticker"
  "sticker": Sticker
}

interface MessageContentText {
  "@type": "messageText"
  "text": FormattedText
  "web_page": WebPage | null
}

interface WebPage {
  "@type": "webPage"
  "url": string
  "display_url": string
  "type": string
  "site_name": string
  "title": string
  "description": FormattedText
  "photo": Photo | null
  "embed_url": string
  "embed_type": string
  "embed_width": number
  "embed_height": number
  "duration": number
  "author": string
  "animation": Animation | null
  "audio": Audio | null
  "document": Document | null
  "sticker": Sticker | null
  "video": Video | null
  "video_note": VideoNote | null
  "voice_note": VoiceNote | null
  "instant_view_version": number
}

interface MessageContentVideo {
  "@type": "messageVideo"
  "video": Video
  "caption": FormattedText
  "is_secret": boolean
}

interface MessageContentVideoChatStarted {
  "@type": "messageVideoChatStarted"
  "group_call_id": number
}

interface MessageContentVideoNote {
  "@type": "messageVideoNote"
  "video_note": VideoNote
  "is_viewed": boolean
  "is_secret": boolean
}

interface Video {
  "@type": "video"
  "duration": number
  "width": number
  "height": number
  "file_name": string
  "mime_type": string
  "has_stickers": boolean
  "supports_streaming": boolean
  "thumbnail": Thumbnail
  "video": TelegramFile
}

interface VideoNote {
  "@type": "videoNote"
  "length": number
  "duration": number
  "thumbnail": Thumbnail
  "video": TelegramFile
}

interface MessageContentVoiceNote {
  "@type": "messageVoiceNote"
  "voice_note": VoiceNote
  "caption": FormattedText
  "is_listened": boolean
}

interface VoiceNote {
  "@type": "voiceNote"
  "duration": number
  "waveform": Uint8Array // XXX
  "mime_type": string
  "voice": TelegramFile
}

interface Document {
  "@type": "document"
  "file_name": string
  "mime_type": string
  "thumbnail": Thumbnail
  "document": TelegramFile
}

interface Thumbnail {
  "@type": "thumbnail"
  "width": number
  "height": number
  "format": ThumbnailFormat
  "file": TelegramFile
}

type ThumbnailFormat =
  | { "@type": "thumbnailFormatGif" }
  | { "@type": "thumbnailFormatJpeg" }
  | { "@type": "thumbnailFormatMpeg4" }
  | { "@type": "thumbnailFormatPng" }
  | { "@type": "thumbnailFormatTgs" }
  | { "@type": "thumbnailFormatWebp" }

interface Audio {
  "@type": "audio"
  "duration": number
  "title": string
  "performer": string
  "file_name": string
  "mime_type": string
  "audio": TelegramFile
}

interface FormattedText {
  "@type": "formattedText"
  "text": string
  "entities": TextEntity[]
}

interface TextEntity {
  "@type": "textEntity"
  "offset": number
  "length": number
  "type": TextEntityType
}

// TextEntityType Class Reference
// Inherited by textEntityTypeBankCardNumber, textEntityTypeBold, textEntityTypeBotCommand, textEntityTypeCashtag, textEntityTypeCode, textEntityTypeEmailAddress, textEntityTypeHashtag, textEntityTypeItalic, textEntityTypeMediaTimestamp, textEntityTypeMention, textEntityTypeMentionName, textEntityTypePhoneNumber, textEntityTypePre, textEntityTypePreCode, textEntityTypeStrikethrough, textEntityTypeTextUrl, textEntityTypeUnderline, and textEntityTypeUrl.

type TextEntityType =
  | { "@type": "textEntityTypeBankCardNumber" }
  | { "@type": "textEntityTypeBold" }
  | { "@type": "textEntityTypeBotCommand" }
  | { "@type": "textEntityTypeCashtag" }
  | { "@type": "textEntityTypeCode" }
  | { "@type": "textEntityTypeEmailAddress" }
  | { "@type": "textEntityTypeHashtag" }
  | { "@type": "textEntityTypeItalic" }
  | { "@type": "textEntityTypeMediaTimestamp" }
  | { "@type": "textEntityTypeMention" }
  | { "@type": "textEntityTypeMentionName" }
  | { "@type": "textEntityTypePhoneNumber" }
  | { "@type": "textEntityTypePre" }
  | { "@type": "textEntityTypePreCode" }
  | { "@type": "textEntityTypeStrikethrough" }
  | { "@type": "textEntityTypeTextUrl" }
  | { "@type": "textEntityTypeUnderline" }
  | { "@type": "textEntityTypeUrl" }

interface AnimatedEmoji {
  "@type": "animatedEmoji"
  "sticker": Sticker
  "fitzpatrick_type": number
  "sound": TelegramFile
}

interface Sticker {
  "@type": "sticker"
  "set_id": bigint
  "sticker": TelegramFile
  "emoji": string
  // some more stuff
}

interface TelegramFile {
  "@type": "file"
  "id": number
  "size": number
  "expected_size": number
  "local": LocalFile
  "remote": RemoteFile
}

interface LocalFile {
  "@type": "localFile"
  "path": string
  "can_be_downloaded": boolean
  "can_be_deleted": boolean
  "is_downloading_active": boolean
  "is_downloading_completed": boolean
  "downloaded_prefix_size": number
  "downloaded_size": number
  "download_offset": number
}

interface RemoteFile {
  "@type": "remoteFile"
  "id": string
  "unique_id": string
  "is_uploading_active": boolean
  "is_uploading_completed": boolean
  "uploaded_size": number
}

type ReplyMarkup =
  | ReplyMarkupInlineKeyboard
  | ReplyMarkupRemoveKeyboard
  | ReplyMarkupForceReply
  | ReplyMarkupShowKeyboard

interface ReplyMarkupInlineKeyboard {
  "@type": "replyMarkupInlineKeyboard"
  "rows": InlineKeyboardButton[][]
}

interface InlineKeyboardButton {
  "@type": "inlineKeyboardButton"
  "text": string
  "type": InlineKeyboardButtonType
  "data": string
  "query": string
}

type InlineKeyboardButtonType =
  | { "@type": "inlineKeyboardButtonTypeCallback"; "data": string }
  | {
      "@type": "inlineKeyboardButtonTypeCallbackWithPassword"
      "data": string
    }
  | { "@type": "inlineKeyboardButtonTypeCallbackGame" }
  | {
      "@type": "inlineKeyboardButtonTypeLoginUrl"
      "url": string
      "id": number
      "forward_text": string
    }
  | {
      "@type": "inlineKeyboardButtonTypeSwitchInline"
      "query": string
      "in_current_chat": boolean
    }
  | { "@type": "inlineKeyboardButtonTypeUrl"; "url": string }
  | { "@type": "inlineKeyboardButtonTypeUser"; "user_id": number }
  | { "@type": "inlineKeyboardButtonTypeBuy" }

interface Message {
  id: number
  sender_id: MessageSender
  chat_id: number
  sending_state: MessageSendingState | null
  scheduling_state: MessageSchedulingState | null
  is_outgoing: boolean
  is_pinned: boolean
  can_be_edited: boolean
  can_be_forwarded: boolean
  can_be_saved: boolean
  can_be_deleted_only_for_self: boolean
  can_be_deleted_for_all_users: boolean
  can_get_statistics: boolean
  can_get_message_thread: boolean
  can_get_viewers: boolean
  can_get_media_timestamp_links: boolean
  has_timestamped_media: boolean
  is_channel_post: boolean
  contains_unread_mention: boolean
  date: number
  edit_date: number
  forward_info: MessageForwardInfo | null
  interaction_info: MessageInteractionInfo | null
  reply_in_chat_id: number
  reply_to_message_id: number
  message_thread_id: number
  ttl: number
  ttl_expires_in: number
  via_bot_user_id: number
  author_signature: string
  media_album_id: bigint // Using bigint for int64 types
  restriction_reason: string
  content: MessageContent | null
  reply_markup: ReplyMarkup | null
}

// for now we define only the most important functions for implementing
// a basic client that can load chats, send messages, download files, etc.
type AnyFunction =
  | GetChat
  | GetChats
  | GetMessage
  | GetMessages
  | SendMessage
  | SendChatAction
  | DownloadFile

interface TelegramFunction<ResultType> {
  "@type": string
}

interface GetChat extends TelegramFunction<Chat> {
  "@type": "getChat"
  "chat_id": number
}

interface GetChats extends TelegramFunction<Chats> {
  "@type": "getChats"
  "chat_list": ChatList | null
  "limit": number
}

interface Chats {
  "@type": "chats"
  "chat_ids": number[]
  "total_count": number
}

interface GetMessage extends TelegramFunction<Message> {
  "@type": "getMessage"
  "chat_id": number
  "message_id": number
}

interface GetMessages extends TelegramFunction<Message[]> {
  "@type": "getMessages"
  "chat_id": number
  "message_ids": number[]
}

interface SendMessage extends TelegramFunction<Message> {
  "@type": "sendMessage"
  "chat_id": number
  "reply_to_message_id": number
  "options": SendMessageOptions | null
  "reply_markup": ReplyMarkup | null
  "input_message_content": InputMessageContent
}

type InputMessageContent =
  | InputMessageText
  | InputMessageAnimation
  | InputMessageAudio
  | InputMessageDocument
  | InputMessagePhoto
  | InputMessageSticker
  | InputMessageVideo
  | InputMessageVideoNote
  | InputMessageVoiceNote

interface InputMessageText {
  "@type": "inputMessageText"
  "text": FormattedText
  "disable_web_page_preview": boolean
  "clear_draft": boolean
}

interface InputMessageAnimation {
  "@type": "inputMessageAnimation"
  "animation": InputFile
  "thumbnail": InputThumbnail | null
  "caption": FormattedText | null
  "duration": number
  "width": number
  "height": number
}

interface InputMessageAudio {
  "@type": "inputMessageAudio"
  "audio": InputFile
  "album_cover_thumbnail": InputThumbnail | null
  "duration": number
  "title": string
  "performer": string
  "caption": FormattedText | null
}

interface InputMessageVoiceNote {
  "@type": "inputMessageVoiceNote"
  "voice_note": InputFile
  "duration": number
  "waveform": Uint8Array // XXX
  "caption": FormattedText | null
}

interface InputMessageVideoNote {
  "@type": "inputMessageVideoNote"
  "video_note": InputFile
  "thumbnail": InputThumbnail | null
  "duration": number
  "length": number
}

interface InputMessageDocument {
  "@type": "inputMessageDocument"
  "document": InputFile
  "thumbnail": InputThumbnail | null
  "caption": FormattedText | null
}

interface InputMessagePhoto {
  "@type": "inputMessagePhoto"
  "photo": InputFile
  "thumbnail": InputThumbnail | null
  "added_sticker_file_ids": number[]
  "width": number
  "height": number
  "caption": FormattedText | null
  "ttl": number
}

interface InputMessageSticker {
  "@type": "inputMessageSticker"
  "sticker": InputFile
  "thumbnail": InputThumbnail | null
  "width": number
  "height": number
}

interface InputMessageVideo {
  "@type": "inputMessageVideo"
  "video": InputFile
  "thumbnail": InputThumbnail | null
  "added_sticker_file_ids": number[]
  "duration": number
  "width": number
  "height": number
  "supports_streaming": boolean
  "caption": FormattedText | null
}

type InputFile =
  | InputFileId
  | InputFileRemote
  | InputFileLocal
  | InputFileGenerated

interface InputFileId {
  "@type": "inputFileId"
  "id": number
}

interface InputFileRemote {
  "@type": "inputFileRemote"
  "id": string
}

interface InputFileLocal {
  "@type": "inputFileLocal"
  "path": string
}

interface InputFileGenerated {
  "@type": "inputFileGenerated"
  "original_path": string
  "conversion": string
  "expected_size": number
}

interface SendMessageOptions {
  "@type": string
  "disable_notification": boolean
  "from_background": boolean
  "scheduling_state": MessageSchedulingState | null
}

interface SendChatAction extends TelegramFunction<Ok> {
  "@type": "sendChatAction"
  "chat_id": number
  "message_thread_id": number
  "action": ChatAction | null
}

type ChatAction =
  | { "@type": "chatActionCancel" }
  | { "@type": "chatActionChoosingContact" }
  | { "@type": "chatActionChoosingLocation" }
  | { "@type": "chatActionChoosingSticker" }
  | { "@type": "chatActionRecordingVideo" }
  | { "@type": "chatActionRecordingVideoNote" }
  | { "@type": "chatActionRecordingVoiceNote" }
  | { "@type": "chatActionStartPlayingGame" }
  | { "@type": "chatActionTyping" }
  | { "@type": "chatActionUploadingDocument"; "progress": number }
  | { "@type": "chatActionUploadingPhoto"; "progress": number }
  | { "@type": "chatActionUploadingVideo"; "progress": number }
  | { "@type": "chatActionUploadingVideoNote"; "progress": number }
  | { "@type": "chatActionUploadingVoiceNote"; "progress": number }
  | { "@type": "chatActionWatchingAnimations"; "emoji": string }
