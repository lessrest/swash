export function tag(tagName, attributes = {}, ...content) {
    const element = document.createElement(tagName);
    for (const [key, value] of Object.entries(attributes)) {
        if (typeof value === "function") {
            element.addEventListener(key.slice(2), value);
        }
        else if (key === "class") {
            element.classList.add(...value.split(" "));
        }
        else if (key === "srcObject") {
            ;
            element.srcObject = value;
        }
        else {
            element.setAttribute(key, value);
        }
    }
    element.append(...content);
    return element;
}
