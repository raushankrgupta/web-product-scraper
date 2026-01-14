document.addEventListener('DOMContentLoaded', () => {
    const notifyBtn = document.getElementById('notifyBtn');
    
    notifyBtn.addEventListener('click', (e) => {
        e.preventDefault();
        
        // Simple animation or feedback
        const originalText = notifyBtn.innerText;
        notifyBtn.innerText = "You're on the list! 🚀";
        notifyBtn.style.background = "linear-gradient(90deg, #00ff88, #00b8ff)";
        notifyBtn.style.boxShadow = "0 0 30px rgba(0, 255, 136, 0.5)";
        notifyBtn.style.pointerEvents = "none"; // Prevent double clicking

        // Reset after a few seconds (optional, but good for demo)
        setTimeout(() => {
            notifyBtn.innerText = originalText;
            notifyBtn.style.background = ""; // Reverts to CSS default
            notifyBtn.style.boxShadow = "";
            notifyBtn.style.pointerEvents = "auto";
        }, 5000);
    });

    // Add a simple entrance animation for cards
    const cards = document.querySelectorAll('.feature-card');
    cards.forEach((card, index) => {
        card.style.opacity = '0';
        card.style.transform = 'translateY(20px)';
        
        setTimeout(() => {
            card.style.transition = 'opacity 0.5s ease, transform 0.5s ease';
            card.style.opacity = '1';
            card.style.transform = 'translateY(0)';
        }, 500 + (index * 200));
    });
});
