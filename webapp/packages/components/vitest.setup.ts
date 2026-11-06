// jsdom lays nothing out, so it has no scrollIntoView (a combobox keeps its
// highlighted option in view with it).
Element.prototype.scrollIntoView = () => undefined;
