document.addEventListener('DOMContentLoaded', () => {
    // Subtle staggered entrance for the "how it works" and "why" cards.
    // Intentionally vanilla JS — no framework, no bundler. The landing
    // page is intended to ship < 50KB total over the wire.
    const animateGroup = (selector, baseDelay = 200) => {
        const els = document.querySelectorAll(selector);
        els.forEach((el, index) => {
            el.style.opacity = '0';
            el.style.transform = 'translateY(20px)';
            setTimeout(() => {
                el.style.transition = 'opacity 0.45s ease, transform 0.45s ease';
                el.style.opacity = '1';
                el.style.transform = 'translateY(0)';
            }, baseDelay + index * 120);
        });
    };

    animateGroup('.how-step', 300);
    animateGroup('.why-card', 200);
});
