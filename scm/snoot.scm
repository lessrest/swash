(use-modules (srfi srfi-9))

(define (swash)
  (call-with-prompt 'foo
	(lambda ()
	  (display "hello")
	  (abort-to-prompt 'foo 1)
	  (display "world"))
	(lambda (k x)
	  (display (list k x))
	  (k)
	  (display "exit"))))

(define next-id!
  (let ((i 0))
	(lambda ()
	  (set! i (+ i 1))
	  i)))

(define task-stack (make-parameter '()))

(define (fyi item)
  (write "[fyi] " )
  (display item)
  (newline))

(define (task thunk)
  (let* ((self (next-id!))
		 (subtasks (list)))
	(define (handle-yield k)
	  (fyi `(task ,self (yield k)))
	  (call-with-prompt
		  'yield
		(lambda () 
		  ;; find next thing to do?
		  ;; should be a queue?
		  ;; a queue of continuations?
		  ;; 
		  ) 
		(lambda (thunk-continuation request)
		  (case (car request)
			((spawn)
			 (set! subtasks (cons (cadr request) subtasks))
			 ;; After spawning, yield.
			 (handle-yield thunk-continuation)
			 )
			((await)
			 (let ((promise (cadr request)))
			   (then promise (lambda (value)
							   (enqueue k value)))
			   )
			 )))))
	(yield )
	
	
	))

